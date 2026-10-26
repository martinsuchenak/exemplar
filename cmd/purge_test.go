package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

func TestPurgeCmdExists(t *testing.T) {
	cmd := purgeCmd()
	if cmd == nil {
		t.Fatal("purgeCmd() should not return nil")
	}
	if cmd.Name != "purge" {
		t.Errorf("expected command name %q, got %q", "purge", cmd.Name)
	}
}

func TestPurgeCommand(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	admin := testutil.Authenticate(t, s.app, s.adminKey)
	q, err := s.app.Datasets.Create(context.Background(), admin, dataset.CreateInput{Name: "inbox", Scope: dataset.ScopeQuarantine})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1.eml", "2.eml"} {
		if _, err := s.app.Fixtures.Create(context.Background(), admin, fixture.CreateInput{DatasetID: q.ID, Name: n, Source: fixture.SourceAnonymised, IngestBatch: "b7", Content: strings.NewReader(n)}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.run(t, s.adminKey, "purge", "b7"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("purge without --yes err = %v", err)
	}
	if _, err := s.run(t, member, "purge", "b7", "--yes"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("member purge err = %v", err)
	}
	out, err := s.run(t, s.adminKey, "purge", "b7", "--yes")
	if err != nil || !strings.Contains(out, "Purged 2 fixtures from ingest batch b7.") {
		t.Fatalf("purge = %q, %v", out, err)
	}
	var res fixture.PurgeResult
	s.runJSON(t, s.adminKey, &res, "purge", "b7", "--yes")
	if res.Deleted != 0 || res.IngestBatch != "b7" {
		t.Errorf("second purge = %+v", res)
	}
}
