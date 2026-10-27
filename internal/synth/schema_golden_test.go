package synth

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// goldenCase is one request's records as the Go implementation made them
// before schemas moved into files (testdata/records-v1.json).
type goldenCase struct {
	Seed    uint64         `json:"seed"`
	People  int            `json:"people"`
	Profile Profile        `json:"profile"`
	Records []goldenRecord `json:"records"`
}

type goldenRecord struct {
	Schema string       `json:"schema"`
	Title  string       `json:"title"`
	People []PersonRole `json:"people"`
	Data   string       `json:"data"`
}

// v1Order is the schema order, and so the spacing, of that implementation.
var v1Order = []RecordSchema{SchemaContact, SchemaCompany, SchemaProject, SchemaOpportunity, SchemaAddress}

// TestBuiltinSchemasMatchTheGoImplementation is the byte-identical port: with
// the old spacing (each schema's place among all five), the built-in files
// give exactly the records the Go code gave, for many seeds, people and both
// profiles, drawing from one generator across a request as generate does.
func TestBuiltinSchemasMatchTheGoImplementation(t *testing.T) {
	raw, err := os.ReadFile("testdata/records-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	set := BuiltinSchemas()
	for _, c := range cases {
		g := New(c.Seed)
		ps := people(g, c.People)
		for i, want := range c.Records {
			slot := i % len(v1Order)
			s, _ := set.Get(v1Order[slot])
			r, err := g.Record(s, RecordInput{Profile: c.Profile, People: ps, Slot: slot, Slots: len(v1Order)}, i/len(v1Order))
			if err != nil {
				t.Fatal(err)
			}
			if string(r.Schema) != want.Schema || r.Title != want.Title || string(r.Data) != want.Data || !reflect.DeepEqual(r.People, want.People) {
				t.Fatalf("seed %d, %d people, %s, record %d (%s) differs:\n got %s %q %v\nwant %s %q %v",
					c.Seed, c.People, c.Profile, i, want.Schema, r.Data, r.Title, r.People, want.Data, want.Title, want.People)
			}
		}
	}
	if len(cases) != 18 {
		t.Errorf("golden cases = %d, want 18", len(cases))
	}
}
