package anonymise

import (
	"reflect"
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
	spans, err := PatternsOnly{}.Detect(ctx, "Ann Lee met ann lee; Annabel did not. Lee.", []string{"Ann Lee", "Lee", ""}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	text := "Ann Lee met ann lee; Annabel did not. Lee."
	for _, s := range spans {
		got = append(got, text[s.Start:s.End])
		if s.Entity != EntityPerson {
			t.Errorf("entity = %s", s.Entity)
		}
	}
	// Whole words in any case: "Annabel" is not Ann.
	if want := []string{"Ann Lee", "ann lee", "Lee", "lee", "Lee"}; !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %q, want %q", got, want)
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
