package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPercentiles(t *testing.T) {
	r := &result{}
	if r.p(50) != 0 || r.max() != 0 {
		t.Error("an empty result has latencies")
	}
	for i := 1; i <= 100; i++ {
		r.lat = append(r.lat, time.Duration(i)*time.Millisecond)
	}
	if r.p(50) != 51*time.Millisecond || r.p(99) != 100*time.Millisecond || r.max() != 100*time.Millisecond {
		t.Errorf("p50 %v p99 %v max %v", r.p(50), r.p(99), r.max())
	}
}

func TestReadKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys")
	if err := os.WriteFile(path, []byte("exm_a\n\n  exm_b  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := readKeys(path)
	if err != nil || strings.Join(keys, ",") != "exm_a,exm_b" {
		t.Errorf("keys = %v, %v", keys, err)
	}
	if _, err := readKeys(""); err == nil {
		t.Error("no -keys accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKeys(empty); err == nil {
		t.Error("a file of no keys accepted")
	}
}

func TestReadFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := readFiles(dir); err == nil {
		t.Error("an empty directory accepted")
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := readFiles(dir)
	if err != nil || len(files) != 1 || files[0].name != "a.txt" || string(files[0].data) != "hello" {
		t.Errorf("files = %+v, %v", files, err)
	}
}

func TestFormatting(t *testing.T) {
	if got := firstLine([]byte("one\ntwo")); got != "one" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine([]byte(strings.Repeat("x", 200))); len(got) != 120 {
		t.Errorf("firstLine is %d long", len(got))
	}
	if got := ms(1500 * time.Microsecond); got != "1.5ms" {
		t.Errorf("ms = %q", got)
	}
}
