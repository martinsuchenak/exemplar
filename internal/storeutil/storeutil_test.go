package storeutil

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/martinsuchenak/exemplar/internal/apperr"
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

func TestSearchTerms(t *testing.T) {
	for query, want := range map[string][]string{
		"":                    nil,
		"  ":                  nil,
		"crm contacts":        {"crm", "contacts"},
		"crm-contacts_v2.csv": {"crm", "contacts", "v2", "csv"},
		"a/b, c\td":           {"a", "b", "c", "d"},
		"Žluťoučký kůň":       {"Žluťoučký", "kůň"},
	} {
		got, err := SearchTerms(query)
		if err != nil || len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("SearchTerms(%q) = %q, %v; want %q", query, got, err, want)
		}
	}
	for _, query := range []string{strings.Repeat("x", maxSearchLen+1), strings.Repeat("w ", maxSearchTerms+1)} {
		if _, err := SearchTerms(query); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("SearchTerms(%d chars) err = %v, want ErrInvalid", len(query), err)
		}
	}
	if got, err := SearchTerms(strings.Repeat("w ", maxSearchTerms)); err != nil || len(got) != maxSearchTerms {
		t.Errorf("SearchTerms at the limit = %d terms, %v", len(got), err)
	}
}

func TestContainsPattern(t *testing.T) {
	for in, want := range map[string]string{
		"crm":  "%crm%",
		"100%": `%100\%%`,
		"a_b":  `%a\_b%`,
		`c:\x`: `%c:\\x%`,
		"":     "%%",
	} {
		if got := ContainsPattern(in); got != want {
			t.Errorf("ContainsPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
