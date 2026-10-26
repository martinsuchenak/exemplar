// Package exchange moves datasets between exemplar instances as archives
// (see archive): export writes chosen datasets with their fixtures, linked
// people and contents; import adds them here. IDs are kept, so importing the
// same archive twice adds nothing the second time. Access is decided by the
// dataset, fixture and person services, exactly as for any other request.
//
// Real-derived fixtures travel only when approved, and arrive approved: the
// review made on the exporting instance is trusted, so importing them needs
// an admin key. Accounts, keys, the audit log and identity mappings never
// travel; whole-instance copies are backups (see backup).
package exchange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/archive"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/db"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/person"
	"github.com/martinsuchenak/exemplar/internal/principal"
)

// MaxDatasets caps how many datasets one export names.
const MaxDatasets = 50

// Record file names inside an export.
const (
	datasetsFile = "datasets.jsonl"
	personsFile  = "persons.jsonl"
	fixturesFile = "fixtures.jsonl"
)

// Service exports and imports datasets.
type Service struct {
	db         xdal.DBInterface
	datasets   *dataset.Service
	fixtures   *fixture.Service
	persons    *person.Service
	audit      *audit.Service
	appVersion string
}

// NewService returns an exchange service built on the other services.
// appVersion is written into export manifests.
func NewService(dbi xdal.DBInterface, datasets *dataset.Service, fixtures *fixture.Service, persons *person.Service, auditService *audit.Service, appVersion string) *Service {
	return &Service{db: dbi, datasets: datasets, fixtures: fixtures, persons: persons, audit: auditService, appVersion: appVersion}
}

// ExportRequest names the datasets to export.
type ExportRequest struct {
	DatasetIDs []string `json:"dataset_ids"`
}

// Export is a prepared export: everything is decided and audited, so only
// writing remains. Its counts are known before the first byte is written.
type Export struct {
	s        *Service
	actor    principal.Principal
	datasets []dataset.Dataset
	fixtures []fixture.Fixture
	persons  []person.Person
	blobs    []blobRef

	// Skipped counts real-derived fixtures left out because they are not
	// approved.
	Skipped int
}

type blobRef struct {
	hash      string
	size      int64
	fixtureID string
}

// Counts returns how many datasets, fixtures, people and blobs the export holds.
func (e *Export) Counts() map[string]int {
	return map[string]int{"datasets": len(e.datasets), "fixtures": len(e.fixtures), "persons": len(e.persons), "blobs": len(e.blobs)}
}

// PrepareExport gathers the datasets the caller can read with their
// shareable fixtures and linked people, and records a dataset.export event
// for each dataset.
func (s *Service) PrepareExport(ctx context.Context, actor principal.Principal, req ExportRequest) (*Export, error) {
	if !actor.CanRead() {
		return nil, fmt.Errorf("%w: this key cannot read", apperr.ErrForbidden)
	}
	ids := unique(req.DatasetIDs)
	if len(ids) == 0 || len(ids) > MaxDatasets {
		return nil, fmt.Errorf("%w: export 1-%d datasets", apperr.ErrInvalid, MaxDatasets)
	}
	e := &Export{s: s, actor: actor}
	perDataset := map[string][2]int{}
	people := map[string]bool{}
	blobs := map[string]bool{}
	for _, id := range ids {
		d, err := s.datasets.Get(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		e.datasets = append(e.datasets, *d)
		all, err := s.allFixtures(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		kept, skipped := 0, 0
		for _, f := range all {
			if !f.Shareable() {
				skipped++
				continue
			}
			kept++
			e.fixtures = append(e.fixtures, f)
			for _, l := range f.People {
				people[l.PersonID] = true
			}
			if !blobs[f.BlobHash] {
				blobs[f.BlobHash] = true
				e.blobs = append(e.blobs, blobRef{hash: f.BlobHash, size: f.Size, fixtureID: f.ID})
			}
		}
		e.Skipped += skipped
		perDataset[id] = [2]int{kept, skipped}
	}
	for _, id := range sortedKeys(people) {
		p, err := s.persons.Get(ctx, actor, id)
		if err != nil {
			return nil, err
		}
		e.persons = append(e.persons, *p)
	}
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		for _, d := range e.datasets {
			n := perDataset[d.ID]
			if err := s.audit.Record(ctx, tx, audit.Event{
				AccountID: actor.AccountID, Action: audit.ActionDatasetExport, TargetType: "dataset", TargetID: d.ID,
				Details: map[string]any{"name": d.Name, "fixtures": n[0], "skipped": n[1]},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Service) allFixtures(ctx context.Context, actor principal.Principal, datasetID string) ([]fixture.Fixture, error) {
	const page = 1000
	var out []fixture.Fixture
	for offset := 0; ; offset += page {
		batch, err := s.fixtures.List(ctx, actor, datasetID, fixture.ListFilter{Limit: page, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// WriteTo writes the archive to w. Contents are read through the fixture
// service, so reads of real-derived content are audit-logged as downloads.
func (e *Export) WriteTo(ctx context.Context, w io.Writer) error {
	aw, err := archive.NewWriter(w, archive.Manifest{
		Kind: archive.KindExport, AppVersion: e.s.appVersion, SchemaVersion: db.LatestVersion(), Counts: e.Counts(),
	})
	if err != nil {
		return err
	}
	if err := archive.Records(aw, datasetsFile, e.datasets); err != nil {
		return err
	}
	if err := archive.Records(aw, personsFile, e.persons); err != nil {
		return err
	}
	if err := archive.Records(aw, fixturesFile, e.fixtures); err != nil {
		return err
	}
	for _, b := range e.blobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		rc, _, err := e.s.fixtures.Open(ctx, e.actor, b.fixtureID)
		if err != nil {
			return err
		}
		err = aw.Blob(b.hash, b.size, rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return aw.Close()
}

// ImportResult says what an import added and what it left out.
type ImportResult struct {
	Datasets     []ImportedDataset `json:"datasets"`
	PeopleAdded  int               `json:"people_added"`
	PeopleMerged int               `json:"people_merged"`
	Skipped      []SkippedFixture  `json:"skipped"`
}

// ImportedDataset is one dataset of an import.
type ImportedDataset struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Scope   dataset.Scope `json:"scope"`
	Version int           `json:"version"`
	// Created is false when the dataset was already here.
	Created bool `json:"created"`
	// Added fixtures are new here; Existing ones were already here.
	Added    int `json:"added"`
	Existing int `json:"existing"`
}

// SkippedFixture is a fixture the import left out, and why.
type SkippedFixture struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DatasetID string `json:"dataset_id"`
	Reason    string `json:"reason"`
}

// Import reads an export archive from r and adds its datasets, people and
// fixtures. Contents are stored as they stream in; records are added in one
// transaction at the end, so a failed import adds no records (stored contents
// nothing uses are removed by the next garbage collection). Fixtures the
// caller may not import (real-derived ones without an admin key) are skipped
// and listed; any other problem fails the whole import.
func (s *Service) Import(ctx context.Context, actor principal.Principal, r io.Reader) (*ImportResult, error) {
	if !actor.CanWrite() {
		return nil, fmt.Errorf("%w: importing needs a write key", apperr.ErrForbidden)
	}
	ar, err := archive.NewReader(r)
	if err != nil {
		return nil, err
	}
	m := ar.Manifest()
	if m.Kind != archive.KindExport {
		return nil, fmt.Errorf("%w: this is a %s archive; restore it with exemplar restore", apperr.ErrInvalid, m.Kind)
	}

	var (
		datasets []dataset.Dataset
		persons  []person.Person
		fixtures []fixture.Fixture
		seen     = map[string]bool{}
	)
	var entry *archive.Entry
	for {
		entry, err = ar.Next()
		if errors.Is(err, io.EOF) {
			entry = nil
			break
		}
		if err != nil {
			return nil, err
		}
		if entry.IsBlob() {
			break
		}
		if seen[entry.Name] {
			return nil, fmt.Errorf("%w: archive: %s appears twice", apperr.ErrInvalid, entry.Name)
		}
		seen[entry.Name] = true
		switch entry.Name {
		case datasetsFile:
			datasets, err = archive.ReadRecords[dataset.Dataset](entry)
		case personsFile:
			persons, err = archive.ReadRecords[person.Person](entry)
		case fixturesFile:
			fixtures, err = archive.ReadRecords[fixture.Fixture](entry)
		default:
			err = fmt.Errorf("%w: archive: an export does not hold %s", apperr.ErrInvalid, entry.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(datasets) != m.Counts["datasets"] || len(persons) != m.Counts["persons"] || len(fixtures) != m.Counts["fixtures"] {
		return nil, fmt.Errorf("%w: archive: records do not match the manifest (truncated?)", apperr.ErrInvalid)
	}

	plan, err := s.planImport(ctx, actor, datasets, persons, fixtures)
	if err != nil {
		return nil, err
	}

	release := s.fixtures.HoldContent()
	defer release()
	sizes := map[string]int64{}
	blobsSeen := 0
	for entry != nil {
		if !entry.IsBlob() {
			return nil, fmt.Errorf("%w: archive: %s after the contents", apperr.ErrInvalid, entry.Name)
		}
		blobsSeen++
		hash := entry.BlobHash()
		if _, done := sizes[hash]; plan.needed[hash] && !done {
			got, size, err := s.fixtures.StoreContent(ctx, actor, entry)
			if err != nil {
				return nil, err
			}
			if got != hash {
				return nil, fmt.Errorf("%w: archive: blob %s does not match its contents", apperr.ErrInvalid, hash)
			}
			sizes[hash] = size
		} else if _, err := io.Copy(io.Discard, entry); err != nil {
			return nil, err
		}
		entry, err = ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if blobsSeen != m.Counts["blobs"] {
		return nil, fmt.Errorf("%w: archive: %d contents, the manifest says %d (truncated?)", apperr.ErrInvalid, blobsSeen, m.Counts["blobs"])
	}
	for hash := range plan.needed {
		if _, ok := sizes[hash]; !ok {
			return nil, fmt.Errorf("%w: archive: content %s is missing", apperr.ErrInvalid, hash)
		}
	}

	res := &ImportResult{Datasets: []ImportedDataset{}, Skipped: plan.skipped}
	err = s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		res.Datasets, res.PeopleAdded, res.PeopleMerged = []ImportedDataset{}, 0, 0
		ids := map[string]string{}
		for _, p := range plan.persons {
			id, added, err := s.persons.Import(ctx, tx, actor, p)
			if err != nil {
				return err
			}
			ids[p.ID] = id
			switch {
			case added:
				res.PeopleAdded++
			case id != p.ID:
				res.PeopleMerged++
			}
		}
		for _, src := range plan.datasets {
			d, created, err := s.datasets.Import(ctx, tx, actor, src)
			if err != nil {
				return err
			}
			out := ImportedDataset{ID: d.ID, Name: d.Name, Scope: d.Scope, Version: d.Version, Created: created}
			for _, f := range plan.byDataset[src.ID] {
				f.Size = sizes[f.BlobHash]
				for i, l := range f.People {
					f.People[i].PersonID = ids[l.PersonID]
				}
				added, err := s.fixtures.Import(ctx, tx, actor, d, f)
				if err != nil {
					return err
				}
				if added {
					out.Added++
				} else {
					out.Existing++
				}
			}
			if created && src.FrozenAt != nil {
				if err := s.datasets.FreezeImported(ctx, tx, d.ID, *src.FrozenAt); err != nil {
					return err
				}
			}
			if err := s.audit.Record(ctx, tx, audit.Event{
				AccountID: actor.AccountID, Action: audit.ActionDatasetImport, TargetType: "dataset", TargetID: d.ID,
				Details: map[string]any{
					"name": d.Name, "scope": d.Scope, "created": created, "added": out.Added, "existing": out.Existing,
					"skipped": plan.skippedIn[src.ID], "exported_at": m.CreatedAt, "exported_by_version": m.AppVersion,
				},
			}); err != nil {
				return err
			}
			res.Datasets = append(res.Datasets, out)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// importPlan is what an import will add, worked out before any content is
// stored.
type importPlan struct {
	datasets  []dataset.Dataset
	persons   []person.Person
	byDataset map[string][]fixture.Fixture
	needed    map[string]bool
	skipped   []SkippedFixture
	skippedIn map[string]int
}

// planImport checks the records hang together and the caller may import
// them, and decides which fixtures to skip.
func (s *Service) planImport(ctx context.Context, actor principal.Principal, datasets []dataset.Dataset, persons []person.Person, fixtures []fixture.Fixture) (*importPlan, error) {
	if len(datasets) == 0 {
		return nil, fmt.Errorf("%w: archive: no datasets", apperr.ErrInvalid)
	}
	plan := &importPlan{byDataset: map[string][]fixture.Fixture{}, needed: map[string]bool{}, skipped: []SkippedFixture{}, skippedIn: map[string]int{}}
	byID := map[string]*dataset.Dataset{}
	for i := range datasets {
		d := &datasets[i]
		if byID[d.ID] != nil {
			return nil, fmt.Errorf("%w: archive: dataset %s appears twice", apperr.ErrInvalid, d.ID)
		}
		if err := s.datasets.CheckImport(ctx, nil, actor, *d); err != nil {
			return nil, err
		}
		byID[d.ID] = d
		plan.datasets = append(plan.datasets, *d)
	}
	people := map[string]person.Person{}
	for _, p := range persons {
		people[p.ID] = p
	}
	linked := map[string]bool{}
	ids := map[string]bool{}
	for _, f := range fixtures {
		d := byID[f.DatasetID]
		if d == nil {
			return nil, fmt.Errorf("%w: archive: fixture %s belongs to no dataset in the archive", apperr.ErrInvalid, f.ID)
		}
		if ids[f.ID] {
			return nil, fmt.Errorf("%w: archive: fixture %s appears twice", apperr.ErrInvalid, f.ID)
		}
		ids[f.ID] = true
		for _, l := range f.People {
			if _, ok := people[l.PersonID]; !ok {
				return nil, fmt.Errorf("%w: archive: fixture %s links person %s, who is not in the archive", apperr.ErrInvalid, f.ID, l.PersonID)
			}
		}
		if err := fixture.CheckImport(actor, d, &f); err != nil {
			if errors.Is(err, apperr.ErrForbidden) {
				plan.skipped = append(plan.skipped, SkippedFixture{ID: f.ID, Name: f.Name, DatasetID: f.DatasetID, Reason: err.Error()})
				plan.skippedIn[f.DatasetID]++
				continue
			}
			return nil, err
		}
		plan.byDataset[f.DatasetID] = append(plan.byDataset[f.DatasetID], f)
		plan.needed[f.BlobHash] = true
		for _, l := range f.People {
			linked[l.PersonID] = true
		}
	}
	for _, id := range sortedKeys(linked) {
		plan.persons = append(plan.persons, people[id])
	}
	return plan, nil
}

func unique(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
