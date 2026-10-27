package synth

import (
	"encoding/json"
	"fmt"
)

// RecordSchema is the kind of structured record.
type RecordSchema string

// The built-in record schemas. Every schema, these included, is defined by a
// file (see schema.go); a server may load more.
const (
	SchemaContact     RecordSchema = "contact"
	SchemaCompany     RecordSchema = "company"
	SchemaProject     RecordSchema = "project"
	SchemaOpportunity RecordSchema = "opportunity"
	SchemaAddress     RecordSchema = "address"
	SchemaProduct     RecordSchema = "product"
	SchemaQuote       RecordSchema = "quote"
	SchemaTask        RecordSchema = "task"
)

// PersonRole links a record or email to a person in it.
type PersonRole struct {
	PersonID string `json:"person_id"`
	Role     string `json:"role"`
}

// Record is one generated structured record.
type Record struct {
	Schema RecordSchema
	// Title names the record, a good basis for a fixture name.
	Title string
	// Data is the record as indented JSON.
	Data []byte
	// People are the registry people the record refers to.
	People []PersonRole
}

// RecordInput is what records are about.
type RecordInput struct {
	Profile Profile
	// People should have IDs from the person registry. Every schema but a
	// catalogue needs at least one.
	People []Person
	// Slot is the schema's place among the Slots schemas of the request, so
	// each schema's records start at a different person.
	Slot, Slots int
}

// companyPerson returns the first person of the index-th distinct company
// among people, in the order the people come. People without a company
// (individuals, such as a sender at a public mail provider) have no company
// record; ok is false when nobody has one.
func companyPerson(people []Person, index int) (p Person, ok bool) {
	var firsts []Person
	seen := map[string]bool{}
	for _, p := range people {
		if p.Company != "" && !seen[p.Company] {
			seen[p.Company] = true
			firsts = append(firsts, p)
		}
	}
	if len(firsts) == 0 {
		return Person{}, false
	}
	return firsts[index%len(firsts)], true
}

// mustJSON encodes v (encoding/json sorts map keys, so output is stable);
// v only holds JSON-safe values.
func mustJSON(v map[string]any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("encoding record: %v", err))
	}
	return append(b, '\n')
}
