package anonymise

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/synth"
)

func TestMemoryRegistry(t *testing.T) {
	m := NewMemoryRegistry()
	if v, ok, err := m.LookupToken(ctx, "h"); ok || v != "" || err != nil {
		t.Errorf("empty lookup = %q, %v, %v", v, ok, err)
	}
	if v, _ := m.Token(ctx, "h", "Ann"); v != "Ann" {
		t.Errorf("first token = %q", v)
	}
	if v, _ := m.Token(ctx, "h", "Bea"); v != "Ann" {
		t.Errorf("token kept = %q", v)
	}
	if v, ok, _ := m.LookupToken(ctx, "h"); !ok || v != "Ann" {
		t.Errorf("lookup = %q, %v", v, ok)
	}
	a, _ := m.Person(ctx, []string{"email:a"}, synth.Person{FirstName: "Ann"})
	b, _ := m.Person(ctx, []string{"name:x", "email:a"}, synth.Person{FirstName: "Bea"})
	c, _ := m.Person(ctx, []string{"name:x"}, synth.Person{FirstName: "Cy"})
	if a.FirstName != "Ann" || b.FirstName != "Ann" || c.FirstName != "Ann" || a.ID != "" {
		t.Errorf("people = %+v %+v %+v", a, b, c)
	}
}

func TestPatternsOnlyFindsKnownNames(t *testing.T) {
	find := func(text string, names ...string) []string {
		t.Helper()
		spans, err := PatternsOnly{}.Detect(ctx, text, names, 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range spans {
			got = append(got, text[s.Start:s.End])
			if s.Entity != EntityPerson {
				t.Errorf("entity = %s", s.Entity)
			}
		}
		return got
	}
	for _, c := range []struct {
		text  string
		names []string
		want  []string
	}{
		// Whole words in any case: "Annabel" is not Ann; the longer name wins.
		{"Ann Lee met ann lee; Annabel did not. Lee.", []string{"Lee", "Ann Lee", ""}, []string{"Ann Lee", "ann lee", "Lee"}},
		// Words end at letters of any script, not only ASCII ones.
		{"José, Zoë and Ólafur; Josée and xZoë are others.", []string{"José", "Zoë", "Ólafur"}, []string{"José", "Zoë", "Ólafur"}},
		// A longer name that runs into a word gives way to a shorter one.
		{"Jo Annex", []string{"Jo", "Jo Ann"}, []string{"Jo"}},
		{"ann_lee 2Ann Ann2 (Ann)", []string{"Ann"}, []string{"Ann"}},
	} {
		if got := find(c.text, c.names...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: spans = %q, want %q", c.text, got, c.want)
		}
	}

	// More names than fit one pattern are all found.
	var names []string
	var text strings.Builder
	for i := range 3*patternsBatch + 7 {
		n := fmt.Sprintf("Name%c%c Person", 'A'+rune(i/26%26), 'a'+rune(i%26)) + strings.Repeat("x", i/676)
		names = append(names, n)
		text.WriteString(n + ". ")
	}
	if got := find(text.String(), names...); len(got) != len(names) {
		t.Errorf("found %d of %d names", len(got), len(names))
	}
}

func TestMediaTypesAndNames(t *testing.T) {
	for _, f := range Formats() {
		if MediaType(f) == "application/octet-stream" {
			t.Errorf("%s has no media type", f)
		}
		name := NameFor(f)
		if got, err := FormatOf(name); err != nil || got != f {
			t.Errorf("NameFor(%s) = %q, which is %q (%v)", f, name, got, err)
		}
	}
	if MediaType("pdf") != "application/octet-stream" || NameFor("pdf") != "" {
		t.Error("an unknown format has a media type or name")
	}
	if MediaType(FormatCSV) != "text/csv" || MediaType(FormatEmail) != "message/rfc822" {
		t.Error("media types")
	}
}

func TestRandomKeysDiffer(t *testing.T) {
	a, err := RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RandomKey()
	if !a.Valid() || a.ID == b.ID || a.Hex("x", "Sarah", 8) == b.Hex("x", "Sarah", 8) {
		t.Errorf("keys %s and %s", a.ID, b.ID)
	}
}

func TestReportIsSorted(t *testing.T) {
	o := &Output{
		Replacements: map[string]string{"Sarah": "Ann", "0412": "0499", "Lane": "Lee"},
		Entities:     map[string]string{"Sarah": EntityPerson, "0412": EntityPhone},
	}
	want := []ReportLine{
		{Entity: "HEADER_NAME", Original: "Lane", Fake: "Lee"},
		{Entity: EntityPerson, Original: "Sarah", Fake: "Ann"},
		{Entity: EntityPhone, Original: "0412", Fake: "0499"},
	}
	if got := o.Report(); !reflect.DeepEqual(got, want) {
		t.Errorf("report = %+v", got)
	}
}
