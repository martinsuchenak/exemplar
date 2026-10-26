package synth

import (
	"encoding/json"
	"fmt"
)

// RecordSchema is the kind of structured record.
type RecordSchema string

// Record schemas.
const (
	SchemaContact     RecordSchema = "contact"
	SchemaCompany     RecordSchema = "company"
	SchemaProject     RecordSchema = "project"
	SchemaOpportunity RecordSchema = "opportunity"
	SchemaAddress     RecordSchema = "address"
)

// Schemas lists every record schema.
var Schemas = []RecordSchema{SchemaContact, SchemaCompany, SchemaProject, SchemaOpportunity, SchemaAddress}

// Valid reports whether s is a known schema.
func (s RecordSchema) Valid() bool {
	for _, v := range Schemas {
		if s == v {
			return true
		}
	}
	return false
}

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
	// People must not be empty and should have IDs from the person registry.
	People []Person
}

// Record generates the index-th record of schema, counting records of that
// schema only. Each schema starts at a different person, spaced evenly
// through People, and steps on one person per record, so records of every
// schema between them cover all the people, and a schema repeats a person
// only after it has used every one. Company records step through the
// distinct companies instead, so no company repeats before all have appeared.
func (g *Gen) Record(schema RecordSchema, in RecordInput, index int) (Record, error) {
	if len(in.People) == 0 {
		return Record{}, fmt.Errorf("a record needs at least one person")
	}
	if !in.Profile.Valid() {
		in.Profile = ProfileInsurance
	}
	p := in.People[personIndex(schema, index, len(in.People))]
	if schema == SchemaCompany {
		p = companyPerson(in.People, index)
	}
	company := CompanyFor(p.Company)
	var rec Record
	switch schema {
	case SchemaContact:
		rec = Record{Title: p.Name(), People: []PersonRole{{p.ID, "contact"}}, Data: mustJSON(map[string]any{
			"schema": schema, "person_id": p.ID, "first_name": p.FirstName, "last_name": p.LastName,
			"email": p.Email, "phone": p.Phone, "job_title": p.JobTitle, "company": p.Company, "address": p.Address,
		})}
	case SchemaCompany:
		var contacts []string
		var roles []PersonRole
		for _, q := range in.People {
			if q.Company == p.Company {
				contacts = append(contacts, q.ID)
				roles = append(roles, PersonRole{q.ID, "contact"})
			}
		}
		rec = Record{Title: company.Name, People: roles, Data: mustJSON(map[string]any{
			"schema": schema, "name": company.Name, "abn": company.ABN, "phone": company.Phone,
			"website": company.Website, "industry": company.Industry, "address": company.Address, "contacts": contacts,
		})}
	case SchemaProject:
		start := g.Date(200)
		title := g.Label(in.Profile) + " - " + trimSuffix(company.Name)
		rec = Record{Title: title, People: []PersonRole{{p.ID, "manager"}}, Data: mustJSON(map[string]any{
			"schema": schema, "name": title, "code": g.Reference(), "company": company.Name, "manager_person_id": p.ID,
			"status":     g.pick([]string{"planned", "active", "on-hold", "completed"}),
			"start_date": start.Format("2006-01-02"), "end_date": start.AddDate(0, g.Between(1, 9), 0).Format("2006-01-02"),
			"budget": g.Money(5000, 250000), "description": g.Paragraph(in.Profile, 1, 2),
		})}
	case SchemaOpportunity:
		title := g.Subject(in.Profile)
		rec = Record{Title: title, People: []PersonRole{{p.ID, "contact"}}, Data: mustJSON(map[string]any{
			"schema": schema, "name": title, "company": company.Name, "contact_person_id": p.ID,
			"stage": g.pick([]string{"lead", "qualified", "proposal", "negotiation", "won", "lost"}),
			"value": g.Money(1000, 150000), "source": g.pick([]string{"referral", "website", "phone", "email", "event", "existing client"}),
			"expected_close": g.Date(90).Format("2006-01-02"), "notes": g.Sentence(in.Profile),
		})}
	case SchemaAddress:
		rec = Record{Title: p.Address.Street, People: []PersonRole{{p.ID, "resident"}}, Data: mustJSON(map[string]any{
			"schema": schema, "person_id": p.ID, "street": p.Address.Street, "suburb": p.Address.Suburb,
			"state": p.Address.State, "postcode": p.Address.Postcode, "country": p.Address.Country,
		})}
	default:
		return Record{}, fmt.Errorf("unknown record schema %q", schema)
	}
	rec.Schema = schema
	return rec, nil
}

// personIndex is the person the index-th record of schema is about. Schema
// number s (in Schemas order) starts at s*ceil(n/len(Schemas)), so with 20
// people the five schemas start at 0, 4, 8, 12 and 16.
func personIndex(schema RecordSchema, index, n int) int {
	pos := 0
	for i, s := range Schemas {
		if s == schema {
			pos = i
		}
	}
	step := (n + len(Schemas) - 1) / len(Schemas)
	return (index + pos*step) % n
}

// companyPerson returns the first person of the index-th distinct company
// among people, in the order the people come.
func companyPerson(people []Person, index int) Person {
	var firsts []Person
	seen := map[string]bool{}
	for _, p := range people {
		if !seen[p.Company] {
			seen[p.Company] = true
			firsts = append(firsts, p)
		}
	}
	return firsts[index%len(firsts)]
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
