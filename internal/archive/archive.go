// Package archive reads and writes exemplar archives, the one format behind
// both export/import and backup/restore: a gzip-compressed tar whose first
// entry is manifest.json, followed by JSON-lines record files, an optional
// database snapshot and content blobs named by their SHA-256 hash.
//
//	manifest.json        what the archive holds (kind, schema version, counts)
//	datasets.jsonl       export: one record per line
//	persons.jsonl
//	fixtures.jsonl
//	exemplar.db          backup: the SQLite snapshot
//	blobs/<sha256>       contents, verified against their name on both sides
//
// Reading rejects anything else: unknown names, paths, links, oversized
// records and blobs whose bytes do not match their hash.
package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

// Format names the archive format in every manifest.
const Format = "exemplar-archive"

// Version is the archive format version this build writes and reads.
const Version = 1

// Kind is what an archive was made for.
type Kind string

// Archive kinds.
const (
	KindExport Kind = "export"
	KindBackup Kind = "backup"
)

// Entry names.
const (
	ManifestName = "manifest.json"
	DatabaseName = "exemplar.db"
	blobPrefix   = "blobs/"
)

// Manifest describes an archive. It is always the first entry.
type Manifest struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	Kind      Kind      `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	// AppVersion is the exemplar build that wrote the archive.
	AppVersion string `json:"app_version,omitempty"`
	// SchemaVersion is the newest database migration of the writer.
	SchemaVersion string `json:"schema_version"`
	// Counts are the records and blobs in the archive, so a reader can tell
	// a truncated archive from a complete one.
	Counts map[string]int `json:"counts"`
}

const (
	maxManifest = 1 << 20
	// MaxRecords caps one JSON-lines entry's size.
	MaxRecords = 64 << 20
	// MaxRecordCount caps the records in one JSON-lines entry, so a
	// compressed archive cannot make a reader hold millions of them.
	MaxRecordCount = 100_000
)

var (
	recordsRe = regexp.MustCompile(`^[a-z_]+\.jsonl$`)
	hashRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: archive: %s", apperr.ErrInvalid, fmt.Sprintf(format, args...))
}

// Writer writes an archive. Call Close to finish it; an archive that was not
// closed is truncated and a reader rejects it.
type Writer struct {
	gz  *gzip.Writer
	tw  *tar.Writer
	now time.Time
}

// NewWriter starts an archive on w with m as its manifest. Format, Version
// and (if zero) CreatedAt are filled in.
func NewWriter(w io.Writer, m Manifest) (*Writer, error) {
	m.Format, m.Version = Format, Version
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	gz := gzip.NewWriter(w)
	aw := &Writer{gz: gz, tw: tar.NewWriter(gz), now: m.CreatedAt}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := aw.write(ManifestName, int64(len(data)), bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return aw, nil
}

// Records writes records as a JSON-lines entry named name (for example
// "fixtures.jsonl").
func Records[T any](w *Writer, name string, records []T) error {
	if !recordsRe.MatchString(name) {
		return fmt.Errorf("archive: bad records name %q", name)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return w.write(name, int64(b.Len()), &b)
}

// Database writes the database snapshot.
func (w *Writer) Database(size int64, r io.Reader) error {
	return w.write(DatabaseName, size, r)
}

// Blob writes one content blob. Its bytes must hash to hash, so a damaged
// store is caught while writing rather than on restore.
func (w *Writer) Blob(hash string, size int64, r io.Reader) error {
	if !hashRe.MatchString(hash) {
		return fmt.Errorf("archive: bad blob hash %q", hash)
	}
	return w.write(blobPrefix+hash, size, Verify(r, hash))
}

func (w *Writer) write(name string, size int64, r io.Reader) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: w.now, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive: writing %s: %w", name, err)
	}
	n, err := io.Copy(w.tw, r)
	if err != nil {
		return fmt.Errorf("archive: writing %s: %w", name, err)
	}
	if n != size {
		return fmt.Errorf("archive: %s is %d bytes, expected %d", name, n, size)
	}
	return nil
}

// Close finishes the archive.
func (w *Writer) Close() error {
	if err := w.tw.Close(); err != nil {
		return err
	}
	return w.gz.Close()
}

// Reader reads an archive entry by entry, in the order written.
type Reader struct {
	tr       *tar.Reader
	manifest Manifest
	limits   Limits
}

// Entry is one archive entry. Read it before calling Next again.
type Entry struct {
	Name string
	Size int64
	io.Reader
}

// BlobHash is the hash a blob entry is named by, or "" for other entries.
func (e *Entry) BlobHash() string {
	return strings.TrimPrefix(e.Name, blobPrefix)
}

// IsBlob reports whether the entry is a content blob.
func (e *Entry) IsBlob() bool {
	return strings.HasPrefix(e.Name, blobPrefix)
}

// Limits bound what a Reader decompresses, so a small, highly compressed
// archive (a gzip bomb) cannot keep a reader busy for hours. Zero means no
// limit.
type Limits struct {
	// Total caps the uncompressed bytes of the whole archive.
	Total int64
	// Blob caps one content entry's size; a larger one is refused from its
	// header, before any of it is read.
	Blob int64
}

// NewReader opens an archive and reads its manifest. Archives of another
// format or a newer format version are rejected.
func NewReader(r io.Reader) (*Reader, error) {
	return NewLimitedReader(r, Limits{})
}

// NewLimitedReader is NewReader within limits; going over one is an
// apperr.ErrTooLarge error.
func NewLimitedReader(r io.Reader, limits Limits) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if errors.Is(err, apperr.ErrTooLarge) {
		return nil, err
	}
	if err != nil {
		return nil, invalid("not a gzip stream")
	}
	var body io.Reader = gz
	if limits.Total > 0 {
		body = &capped{r: gz, left: limits.Total, limit: limits.Total}
	}
	ar := &Reader{tr: tar.NewReader(body), limits: limits}
	hdr, err := ar.tr.Next()
	if errors.Is(err, apperr.ErrTooLarge) {
		return nil, err
	}
	if err != nil || hdr.Name != ManifestName || hdr.Size > maxManifest {
		return nil, invalid("the first entry must be %s", ManifestName)
	}
	if err := json.NewDecoder(io.LimitReader(ar.tr, maxManifest)).Decode(&ar.manifest); err != nil {
		return nil, invalid("unreadable manifest")
	}
	m := ar.manifest
	if m.Format != Format {
		return nil, invalid("not an exemplar archive")
	}
	if m.Version < 1 || m.Version > Version {
		return nil, invalid("format version %d is not supported (this build reads %d)", m.Version, Version)
	}
	if m.Kind != KindExport && m.Kind != KindBackup {
		return nil, invalid("unknown kind %q", m.Kind)
	}
	return ar, nil
}

// Manifest returns the archive's manifest.
func (r *Reader) Manifest() Manifest {
	return r.manifest
}

// Next returns the next entry, or io.EOF after the last one.
func (r *Reader) Next() (*Entry, error) {
	hdr, err := r.tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}
	if errors.Is(err, apperr.ErrTooLarge) {
		return nil, err
	}
	if err != nil {
		return nil, invalid("damaged or truncated (%v)", err)
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil, invalid("%s is not a regular file", hdr.Name)
	}
	name := hdr.Name
	switch {
	case name == DatabaseName, recordsRe.MatchString(name):
	case strings.HasPrefix(name, blobPrefix) && hashRe.MatchString(strings.TrimPrefix(name, blobPrefix)):
	default:
		return nil, invalid("unexpected entry %q", name)
	}
	if hdr.Size < 0 {
		return nil, invalid("%s has a negative size", name)
	}
	if r.limits.Blob > 0 && hdr.Size > r.limits.Blob && strings.HasPrefix(name, blobPrefix) {
		return nil, fmt.Errorf("%w: archive: %s is %d bytes, over the %d-byte content limit", apperr.ErrTooLarge, name, hdr.Size, r.limits.Blob)
	}
	var body io.Reader = damaged{r: r.tr, name: name}
	if strings.HasPrefix(name, blobPrefix) {
		body = Verify(body, strings.TrimPrefix(name, blobPrefix))
	}
	return &Entry{Name: name, Size: hdr.Size, Reader: body}, nil
}

// capped fails once more than limit uncompressed bytes have been read.
type capped struct {
	r           io.Reader
	left, limit int64
}

// Read implements io.Reader.
func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("%w: archive: over %d bytes uncompressed", apperr.ErrTooLarge, c.limit)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// damaged reports a read error inside an entry (a truncated or corrupt
// stream) as invalid input rather than a server fault.
type damaged struct {
	r    io.Reader
	name string
}

// Read implements io.Reader.
func (d damaged) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, apperr.ErrInvalid) && !errors.Is(err, apperr.ErrTooLarge) {
		err = invalid("%s is damaged or truncated (%v)", d.name, err)
	}
	return n, err
}

// ReadRecords decodes a JSON-lines entry, refusing one over MaxRecords.
func ReadRecords[T any](e *Entry) ([]T, error) {
	if e.Size > MaxRecords {
		return nil, invalid("%s is over %d bytes", e.Name, MaxRecords)
	}
	dec := json.NewDecoder(io.LimitReader(e, MaxRecords))
	var out []T
	for {
		var v T
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if errors.Is(err, apperr.ErrTooLarge) {
			return nil, err
		}
		if err != nil {
			return nil, invalid("%s: %v", e.Name, err)
		}
		if len(out) == MaxRecordCount {
			return nil, invalid("%s holds over %d records", e.Name, MaxRecordCount)
		}
		out = append(out, v)
	}
}

// Verify returns a reader that fails at the end of r unless its bytes hash
// to hash.
func Verify(r io.Reader, hash string) io.Reader {
	return &verifier{r: r, h: sha256.New(), want: hash}
}

type verifier struct {
	r    io.Reader
	h    hash.Hash
	want string
}

// Read implements io.Reader, checking the hash once r is exhausted.
func (v *verifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if n > 0 {
		_, _ = v.h.Write(p[:n])
	}
	if errors.Is(err, io.EOF) {
		if got := hex.EncodeToString(v.h.Sum(nil)); got != v.want {
			return n, invalid("blob %s does not match its contents (%s)", v.want, got)
		}
	}
	return n, err
}
