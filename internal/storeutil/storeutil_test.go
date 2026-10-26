package storeutil

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewIDIsUniqueAndSortable(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b || len(a) != 36 {
		t.Errorf("ids %q %q", a, b)
	}
	if a > b {
		t.Errorf("UUIDv7 ids should sort by creation: %q > %q", a, b)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	in := time.Date(2026, 10, 26, 8, 30, 0, 123456789, time.FixedZone("AWST", 8*3600))
	s := FormatTime(in)
	if s != "2026-10-26T00:30:00.123456789Z" {
		t.Errorf("FormatTime = %q", s)
	}
	if out := ParseTime(s); !out.Equal(in) {
		t.Errorf("ParseTime = %v, want %v", out, in)
	}
	if !ParseTime("garbage").IsZero() {
		t.Error("ParseTime of garbage should be zero")
	}
}

func TestNullTimeAndTimeArg(t *testing.T) {
	if NullTime(sql.NullString{}) != nil || NullTime(sql.NullString{Valid: true}) != nil {
		t.Error("NullTime of null or empty should be nil")
	}
	got := NullTime(sql.NullString{Valid: true, String: "2026-10-26T00:00:00Z"})
	if got == nil || got.Year() != 2026 {
		t.Errorf("NullTime = %v", got)
	}
	if TimeArg(nil) != nil {
		t.Error("TimeArg(nil) should be nil")
	}
	if v := TimeArg(got); v != "2026-10-26T00:00:00Z" {
		t.Errorf("TimeArg = %v", v)
	}
}

func TestStringsColumn(t *testing.T) {
	if EncodeStrings(nil) != "[]" {
		t.Errorf("EncodeStrings(nil) = %q", EncodeStrings(nil))
	}
	in := []string{"a", "b c"}
	if out := DecodeStrings(EncodeStrings(in)); !reflect.DeepEqual(out, in) {
		t.Errorf("round trip = %v", out)
	}
	for _, bad := range []string{"", "null", "{"} {
		if out := DecodeStrings(bad); out == nil || len(out) != 0 {
			t.Errorf("DecodeStrings(%q) = %v, want empty slice", bad, out)
		}
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if IsUniqueViolation(nil) || IsUniqueViolation(errors.New("other")) {
		t.Error("false positive")
	}
	if !IsUniqueViolation(errors.New("constraint failed: UNIQUE constraint failed: accounts.name (2067)")) {
		t.Error("missed a unique violation")
	}
}

func TestValidID(t *testing.T) {
	for id, ok := range map[string]bool{
		NewID(): true, "f1": true, "other-dataset": true,
		"": false, "../../etc/passwd": false, "a/b": false, "a%2Fb": false, "a b": false, "a.b": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	} {
		if ValidID(id) != ok {
			t.Errorf("ValidID(%q) != %v", id, ok)
		}
	}
}
