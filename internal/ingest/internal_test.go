package ingest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

func TestStoreErrorHidesInternals(t *testing.T) {
	conflict := fmt.Errorf("%w: name taken", apperr.ErrConflict)
	if got := storeError(conflict); got != conflict.Error() {
		t.Errorf("conflict = %q", got)
	}
	if got := storeError(errors.New("open /var/lib/exemplar/blobs/ab: disk full")); got != "storing the fixture failed" {
		t.Errorf("internal = %q", got)
	}
}

func TestBatchIDs(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 15, 2, 0, time.UTC)
	a, b := newBatchID(now), newBatchID(now)
	if a == b || !batchRe.MatchString(a) || a[:len("ingest-20261026-091502-")] != "ingest-20261026-091502-" {
		t.Errorf("batch ids = %s, %s", a, b)
	}
}
