package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

type rec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Manifest{Kind: KindExport, SchemaVersion: "1", Counts: map[string]int{"things": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Records(w, "things.jsonl", []rec{{"1", "a"}, {"2", "b"}}); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello")
	if err := w.Blob(sum(content), int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Database(3, strings.NewReader("db!")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	m := r.Manifest()
	if m.Format != Format || m.Version != Version || m.Kind != KindExport || m.Counts["things"] != 2 || m.CreatedAt.IsZero() {
		t.Errorf("manifest = %+v", m)
	}
	e, _ := r.Next()
	got, err := ReadRecords[rec](e)
	if err != nil || len(got) != 2 || got[1].Name != "b" || e.IsBlob() {
		t.Errorf("records = %+v, %v", got, err)
	}
	e, _ = r.Next()
	data, err := io.ReadAll(e)
	if err != nil || string(data) != "hello" || !e.IsBlob() || e.BlobHash() != sum(content) {
		t.Errorf("blob = %q, %v", data, err)
	}
	e, _ = r.Next()
	if e.Name != DatabaseName {
		t.Errorf("entry = %s", e.Name)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("end = %v", err)
	}
}

func TestWriterRefusesBadInput(t *testing.T) {
	w, _ := NewWriter(io.Discard, Manifest{Kind: KindBackup})
	if err := Records(w, "../x.jsonl", []rec{}); err == nil {
		t.Error("bad records name accepted")
	}
	if err := w.Blob("nothex", 1, strings.NewReader("x")); err == nil {
		t.Error("bad hash accepted")
	}
	if err := w.Blob(sum([]byte("a")), 1, strings.NewReader("b")); err == nil || !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("content not matching its hash: %v", err)
	}
	w, _ = NewWriter(io.Discard, Manifest{Kind: KindBackup})
	if err := w.Database(10, strings.NewReader("short")); err == nil {
		t.Error("short entry accepted")
	}
}

// raw builds a tar.gz by hand, for archives a Writer would never make.
func raw(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e[0], Mode: 0o600, Size: int64(len(e[1])), Typeflag: tar.TypeReg, ModTime: time.Now()}
		if strings.HasPrefix(e[0], "link:") {
			hdr = &tar.Header{Name: strings.TrimPrefix(e[0], "link:"), Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, ModTime: time.Now()}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e[1]))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func manifest(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestReaderRejects(t *testing.T) {
	good := manifest(map[string]any{"format": Format, "version": 1, "kind": "export"})
	for name, data := range map[string][]byte{
		"not gzip":      []byte("plain"),
		"no manifest":   raw(t, [2]string{"datasets.jsonl", "{}"}),
		"bad manifest":  raw(t, [2]string{ManifestName, "{"}),
		"other format":  raw(t, [2]string{ManifestName, manifest(map[string]any{"format": "zip", "version": 1, "kind": "export"})}),
		"newer version": raw(t, [2]string{ManifestName, manifest(map[string]any{"format": Format, "version": 2, "kind": "export"})}),
		"unknown kind":  raw(t, [2]string{ManifestName, manifest(map[string]any{"format": Format, "version": 1, "kind": "zip"})}),
		"empty":         raw(t),
	} {
		if _, err := NewReader(bytes.NewReader(data)); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, entry := range map[string][2]string{
		"path":       {"../../etc/passwd", "x"},
		"nested":     {"blobs/ab/cd", "x"},
		"unknown":    {"notes.txt", "x"},
		"symlink":    {"link:exemplar.db", ""},
		"upper hash": {"blobs/" + strings.ToUpper(sum([]byte("x"))), "x"},
	} {
		r, err := NewReader(bytes.NewReader(raw(t, [2]string{ManifestName, good}, entry)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Next(); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	r, _ := NewReader(bytes.NewReader(raw(t, [2]string{ManifestName, good}, [2]string{"blobs/" + sum([]byte("a")), "b"})))
	e, _ := r.Next()
	if _, err := io.ReadAll(e); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("tampered blob read without error: %v", err)
	}

	r, _ = NewReader(bytes.NewReader(raw(t, [2]string{ManifestName, good}, [2]string{"x.jsonl", "{\"id\":1}\nnot json"})))
	e, _ = r.Next()
	if _, err := ReadRecords[rec](e); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("bad records err = %v", err)
	}

	full := raw(t, [2]string{ManifestName, good}, [2]string{"x.jsonl", strings.Repeat("{\"id\":\"0123456789\"}\n", 20000)})
	truncated, err := NewReader(bytes.NewReader(full[:len(full)/2]))
	if err == nil {
		for {
			e, err := truncated.Next()
			if err == nil {
				_, err = io.ReadAll(e)
			}
			if errors.Is(err, io.EOF) {
				t.Fatal("truncated archive read to the end")
			}
			if err != nil {
				break
			}
		}
	}
}

func TestRecordCountLimit(t *testing.T) {
	good := manifest(map[string]any{"format": Format, "version": 1, "kind": "export"})
	r, _ := NewReader(bytes.NewReader(raw(t, [2]string{ManifestName, good}, [2]string{"x.jsonl", strings.Repeat("{}\n", MaxRecordCount+1)})))
	e, _ := r.Next()
	if _, err := ReadRecords[rec](e); !errors.Is(err, apperr.ErrInvalid) || !strings.Contains(err.Error(), "records") {
		t.Errorf("too many records err = %v", err)
	}
}
