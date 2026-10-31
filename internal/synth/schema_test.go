package synth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSchema writes one schema file named after name into dir.
func writeSchema(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const minimal = `name = "ticket"
title = "{subject}"

[[links]]
role = "reporter"
value = "person.id"

[[fields]]
name = "subject"
text = "subject"
`

func TestBuiltinSchemasLoad(t *testing.T) {
	set := BuiltinSchemas()
	var names []string
	for _, n := range set.Names() {
		names = append(names, string(n))
	}
	// The one place the built-in set is pinned; other tests count it.
	if strings.Join(names, ",") != "address,company,contact,opportunity,product,project,quote,task" {
		t.Errorf("built-in schemas = %v", names)
	}
	for _, s := range set.List() {
		if !s.Builtin || len(s.Hash) != 8 || s.Description == "" || len(s.Fields) == 0 {
			t.Errorf("%s = %+v", s.Name, s)
		}
	}
	quote, _ := set.Get(SchemaQuote)
	if strings.Join(quote.Fields, ",") != "quote_number,name,company,contact_person_id,status,issued,valid_until,items,subtotal,gst,total,notes" {
		t.Errorf("quote fields = %v", quote.Fields)
	}
}

func TestSchemaDirectoryAddsAndReplaces(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "ticket", minimal)
	writeSchema(t, dir, "address", strings.Replace(minimal, `name = "ticket"`, `name = "address"`, 1))
	set, replaced, err := LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(replaced, ",") != "address" {
		t.Errorf("replaced = %v", replaced)
	}
	ticket, ok := set.Get("ticket")
	address, _ := set.Get(SchemaAddress)
	if !ok || ticket.Builtin || address.Builtin || strings.Join(address.Fields, ",") != "subject" {
		t.Errorf("ticket %+v, address %+v", ticket, address)
	}
	if builtin, _ := BuiltinSchemas().Get(SchemaAddress); builtin.Hash == address.Hash {
		t.Error("a different file has the same hash")
	}

	g := New(1)
	r, err := g.Record(ticket, RecordInput{People: people(g, 2)}, 0)
	if err != nil || r.Title == "" || len(r.People) != 1 || r.People[0].Role != "reporter" {
		t.Errorf("ticket = %+v, %v", r, err)
	}

	// No directory, a missing one or an empty one: the built-ins only.
	for _, d := range []string{"", t.TempDir(), filepath.Join(dir, "missing")} {
		if set, _, err := LoadSchemas(d); err != nil || len(set.Names()) != len(BuiltinSchemas().Names()) {
			t.Errorf("dir %q: %v schemas, %v", d, len(set.Names()), err)
		}
	}
}

// TestSchemaValidation refuses each kind of mistake when the file is loaded,
// naming the file and what is wrong.
func TestSchemaValidation(t *testing.T) {
	field := func(lines string) string { return minimal + "\n[[fields]]\n" + lines + "\n" }
	tests := []struct {
		name, body, want string
		file             string
	}{
		{"bad toml", "name = ", "", "ticket"},
		{"unknown key", "colour = \"red\"\n" + minimal, `unknown key "colour"`, "ticket"},
		{"bad name", strings.Replace(minimal, `"ticket"`, `"Ticket"`, 1), "lower-case", "ticket"},
		{"file name", minimal, "must be named ticket.toml", "other"},
		{"bad step", strings.Replace(minimal, "title", "step = \"daily\"\ntitle", 1), "step must be", "ticket"},
		{"no title", strings.Replace(minimal, `title = "{subject}"`, "", 1), "title is required", "ticket"},
		{"title name", strings.Replace(minimal, `{subject}`, `{nothing}`, 1), `"nothing" is not a field`, "ticket"},
		{"link role", strings.Replace(minimal, `role = "reporter"`, "", 1), "needs a role", "ticket"},
		{"link value", strings.Replace(minimal, `value = "person.id"`, `value = "person.email"`, 1), "person.id or company.contacts", "ticket"},
		{"two types", field("name = \"x\"\nint = [1, 2]\nmoney = [1, 2]"), "exactly one type key", "ticket"},
		{"no type", field("name = \"x\""), "exactly one type key", "ticket"},
		{"field name", field("name = \"1x\"\nint = [1, 2]"), "starting with a letter", "ticket"},
		{"schema field", field("name = \"schema\"\nint = [1, 2]"), "set on every record", "ticket"},
		{"twice", field("name = \"subject\"\nint = [1, 2]"), "defined twice", "ticket"},
		{"later field", field("name = \"x\"\nvalue = \"y\"") + "\n[[fields]]\nname = \"y\"\nint = [1, 2]\n", `"y" is not a field above`, "ticket"},
		{"unknown attribute", field("name = \"x\"\nvalue = \"person.salary\""), "unknown attribute", "ticket"},
		{"unknown prefix", field("name = \"x\"\nvalue = \"boss.name\""), "person.*, company.*", "ticket"},
		{"address part", field("name = \"x\"\nvalue = \"person.address.planet\""), "unknown attribute", "ticket"},
		{"bad placeholder", field("name = \"x\"\ntemplate = \"{bad name}\""), "malformed placeholder", "ticket"},
		{"ref prefix", field("name = \"x\"\nref = \"qu\""), "capital letters", "ticket"},
		{"empty pick", field("name = \"x\"\npick = []"), "pick list is empty", "ticket"},
		{"pick date", field("name = \"x\"\npick = [2026-01-02]"), "write a date as a string", "ticket"},
		{"pick word", field("name = \"x\"\npick = \"words\""), "\"@name\"", "ticket"},
		{"missing list", field("name = \"x\"\npick = \"@colours\""), `list "colours" is missing for profile`, "ticket"},
		{"list profile", minimal + "\n[lists.legal]\nx = [\"a\"]\n", "unknown profile", "ticket"},
		{"empty list", minimal + "\n[lists.all]\nx = []\n", "is empty", "ticket"},
		{"range order", field("name = \"x\"\nint = [5, 1]"), "low <= high", "ticket"},
		{"range size", field("name = \"x\"\nint = [5]"), "low <= high", "ticket"},
		{"negative money", field("name = \"x\"\nmoney = [-5, 1]"), "must not be negative", "ticket"},
		{"money format", field("name = \"x\"\nmoney = [1, 5]\nformat = \"euro\""), "display, number or string", "ticket"},
		{"format on text", field("name = \"x\"\ntext = \"sentence\"\nformat = \"number\""), "only for money, int and date", "ticket"},
		{"date shape", field("name = \"x\"\ndate = { days = [1, 2] }"), "within_days", "ticket"},
		{"date negative", field("name = \"x\"\ndate = { within_days = -1 }"), "must not be negative", "ticket"},
		{"date after", field("name = \"x\"\ndate = { after = \"subject\", days = [1, 2] }"), "not a date field", "ticket"},
		{"days and months", field("name = \"d\"\ndate = { within_days = 5 }") + "\n[[fields]]\nname = \"x\"\ndate = { after = \"d\", days = [1, 2], months = [1, 2] }\n", "not both", "ticket"},
		{"text kind", field("name = \"x\"\ntext = \"poem\""), "text must be", "ticket"},
		{"paragraph", field("name = \"x\"\ntext = { paragraph = [0, 1] }"), "min at least 1", "ticket"},
		{"paragraph shape", field("name = \"x\"\ntext = { essay = 1 }"), "paragraph takes", "ticket"},
		{"sum of text", field("name = \"x\"\nsum = \"subject\""), "not a money field", "ticket"},
		{"sum empty", field("name = \"x\"\nsum = []"), "needs a field", "ticket"},
		{"sum column", field("name = \"x\"\nsum = \"subject.amount\""), "money column", "ticket"},
		{"percent of", field("name = \"x\"\npercent = { of = \"subject\", rate = 10 }"), "not a money field", "ticket"},
		{"percent rate", field("name = \"m\"\nmoney = [1, 2]") + "\n[[fields]]\nname = \"x\"\npercent = { of = \"m\", rate = -1 }\n", "rate must be", "ticket"},
		{"lines catalogue", field("name = \"x\"\nlines = { catalogue = \"\", count = [1, 2], qty = [1, 2] }"), "needs a catalogue", "ticket"},
		{"lines count", field("name = \"x\"\nlines = { catalogue = \"product\", count = [0, 2], qty = [1, 2] }"), "at least 1", "ticket"},
		{"lines not catalogue", field("name = \"x\"\nlines = { catalogue = \"contact\", count = [1, 2], qty = [1, 2] }"), "not a catalogue schema", "ticket"},
		{"nth outside", field("name = \"x\"\nnth = \"@a\""), "only for catalogue entries", "ticket"},
		{"catalogue table", minimal + "\n[catalogue]\nsize = 3\n", "only for step", "ticket"},
		{"catalogue size", strings.Replace(minimal, "title", "step = \"catalogue\"\ntitle", 1), "size of 1-10000", "ticket"},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		writeSchema(t, dir, tt.file, tt.body)
		_, _, err := LoadSchemas(dir)
		if err == nil || !strings.Contains(err.Error(), tt.file+".toml") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want the file and %q", tt.name, err, tt.want)
		}
	}
}

func TestCatalogueValidation(t *testing.T) {
	const catalogue = `name = "part"
step = "catalogue"
title = "{name}"

[catalogue]
size = 3

[[catalogue.fields]]
name = "name"
nth = "@part"

[[catalogue.fields]]
name = "unit_price"
money = [1, 9]

[lists.all]
part = ["bolt", "nut", "washer"]
`
	tests := []struct{ name, body, want string }{
		{"links", catalogue + "\n[[links]]\nrole = \"x\"\nvalue = \"person.id\"\n", "link no people"},
		{"person in entry", strings.Replace(catalogue, `nth = "@part"`, `value = "person.name"`, 1), "about no person"},
		{"lines in entry", strings.Replace(catalogue, `nth = "@part"`, `lines = { catalogue = "part", count = [1, 2], qty = [1, 2] }`, 1), "cannot have lines"},
		{"price not money", strings.Replace(catalogue, "money = [1, 9]", "int = [1, 9]", 1), "unit_price must be money"},
		{"size over list", strings.Replace(catalogue, "size = 3", "size = 4", 1), "entries would repeat"},
		{"nth word", strings.Replace(catalogue, `nth = "@part"`, `nth = "part"`, 1), "\"@name\""},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		writeSchema(t, dir, "part", tt.body)
		if _, _, err := LoadSchemas(dir); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}

	// A quote naming a catalogue without a unit_price is refused once every
	// file is read.
	dir := t.TempDir()
	writeSchema(t, dir, "part", strings.Replace(catalogue, "unit_price", "weight", 1))
	writeSchema(t, dir, "order", strings.Replace(minimal, `"ticket"`, `"order"`, 1)+
		"\n[[fields]]\nname = \"items\"\nlines = { catalogue = \"part\", count = [1, 2], qty = [1, 2] }\n")
	if _, _, err := LoadSchemas(dir); err == nil || !strings.Contains(err.Error(), "no unit_price") {
		t.Errorf("catalogue without prices err = %v", err)
	}
	// The same file twice under different names is refused too.
	dir = t.TempDir()
	writeSchema(t, dir, "ticket", minimal)
	if err := os.WriteFile(filepath.Join(dir, "copy.toml"), []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSchemas(dir); err == nil {
		t.Error("a file named after another schema was accepted")
	}
}

// TestFieldTypes runs every field type through one file and checks what each
// gives.
func TestFieldTypes(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "sample", `name = "sample"
title = "{ref} for {person.name}"

[[links]]
role = "member"
value = "company.contacts"

[[fields]]
name = "ref"
ref = "SM"

[[fields]]
name = "any_ref"
ref = ""

[[fields]]
name = "who"
template = "{person.first_name} at {company.short} ({company.abn}), {label}"

[[fields]]
name = "street"
value = "company.address.street"

[[fields]]
name = "colour"
pick = "@colour"

[[fields]]
name = "size"
pick = ["s", "m"]

[[fields]]
name = "count"
int = [3, 3]

[[fields]]
name = "price"
money = [10, 10]

[[fields]]
name = "price_number"
money = [12, 12]
format = "number"

[[fields]]
name = "both"
sum = ["price", "price_number"]
format = "number"

[[fields]]
name = "tax"
percent = { of = "both", rate = 12.5 }

[[fields]]
name = "start"
date = { within_days = 0 }
format = "02/01/2006"

[[fields]]
name = "later"
date = { after = "start", days = [3, 3] }

[[fields]]
name = "much_later"
date = { after = "start", months = [2, 2] }

[[fields]]
name = "note"
text = { paragraph = [2, 0] }

[[fields]]
name = "heading"
text = "title"

[[fields]]
name = "contacts"
value = "company.contacts"

[lists.insurance]
colour = ["blue"]

[lists.all]
colour = ["red"]
`)
	set, _, err := LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := set.Get("sample")
	g := New(3)
	ps := people(g, 4) // companies: p1 alone, p2 and p3, p4 alone
	ps[2].Company = ps[1].Company
	for _, profile := range Profiles {
		r, err := g.Record(s, RecordInput{Profile: profile, People: ps, Slot: 1, Slots: 4}, 0)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(r.Data, &doc); err != nil {
			t.Fatal(err)
		}
		// Slot 1 of 4 with 4 people starts at the second person.
		p := ps[1]
		co := CompanyFor(p.Company)
		want := map[string]any{
			"count": 3.0, "price": "$10.00", "price_number": 12.0, "both": 22.0, "tax": "$2.75",
			"start": "15/01/2026", "later": "2026-01-18", "much_later": "2026-03-15", "size": nil,
		}
		for k, v := range want {
			if v != nil && doc[k] != v {
				t.Errorf("%s: %s = %v, want %v", profile, k, doc[k], v)
			}
		}
		colour := map[Profile]string{ProfileInsurance: "blue", ProfileManufacturing: "red"}[profile]
		if doc["colour"] != colour || (doc["size"] != "s" && doc["size"] != "m") || doc["street"] != co.Address.Street {
			t.Errorf("%s: colour %v, size %v, street %v", profile, doc["colour"], doc["size"], doc["street"])
		}
		if ref := doc["ref"].(string); !strings.HasPrefix(ref, "SM-") || len(ref) != 9 || !refRe(doc["any_ref"].(string)) {
			t.Errorf("refs %v %v", doc["ref"], doc["any_ref"])
		}
		who := doc["who"].(string)
		if !strings.HasPrefix(who, p.FirstName+" at "+trimSuffix(co.Name)+" ("+co.ABN+"), ") || len(who) <= len(p.FirstName)+10 {
			t.Errorf("who = %q", who)
		}
		if n := strings.Count(doc["note"].(string), "."); n < 2 || doc["heading"] == "" {
			t.Errorf("note %q, heading %v", doc["note"], doc["heading"])
		}
		if c := doc["contacts"].([]any); len(c) != 2 || c[0] != "p2" || c[1] != "p3" || len(r.People) != 2 || r.People[1].PersonID != "p3" {
			t.Errorf("contacts %v, links %v", c, r.People)
		}
		if r.Title != doc["ref"].(string)+" for "+p.Name() {
			t.Errorf("title = %q", r.Title)
		}
	}
}

func refRe(s string) bool {
	parts := strings.Split(s, "-")
	return len(parts) == 2 && len(parts[1]) == 6 && parts[0] != ""
}

// TestCatalogueEntriesAreFixed pins one product so a change to how entries
// are derived (which would break agreement with existing quotes) shows up.
func TestCatalogueEntriesAreFixed(t *testing.T) {
	product, _ := BuiltinSchemas().Get(SchemaProduct)
	a := product.entry(ProfileManufacturing, 0)
	b := product.entry(ProfileManufacturing, 0)
	if a["sku"].text != b["sku"].text || a["unit_price"].cents != b["unit_price"].cents {
		t.Error("an entry changed between calls")
	}
	if a["name"].text != "Laser-cut steel bracket" || a["unit"].text != "each" || a["category"].text != "Fabrication" {
		t.Errorf("entry 0 = %v %v %v", a["name"].text, a["unit"].text, a["category"].text)
	}
	if other := product.entry(ProfileInsurance, 0); other["name"].text != "Public liability cover" || other["sku"].text == a["sku"].text {
		t.Errorf("insurance entry 0 = %v %v", other["name"].text, other["sku"].text)
	}
	if a["sku"].text != "PRD-"+pinnedSKU {
		t.Errorf("entry 0 SKU = %s; entries are derived differently, which breaks agreement with existing quotes", a["sku"].text)
	}
	// Product records go through the catalogue in order, wrapping.
	g := New(1)
	r, _ := g.Record(product, RecordInput{Profile: ProfileManufacturing}, 24)
	if !strings.Contains(string(r.Data), "Laser-cut steel bracket") || len(r.People) != 0 {
		t.Errorf("record 24 = %s", r.Data)
	}
}

// pinnedSKU is manufacturing product entry 0's SKU number.
const pinnedSKU = "206163"

// TestPersonWithoutACompany checks every company value reads as empty for an
// individual (no company made up from an empty name), that a fallback
// placeholder names them instead, and that company contacts link no one.
func TestPersonWithoutACompany(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "visit", `name = "visit"
title = "{company.short|person.name} {company.name|label}"

[[links]]
role = "colleague"
value = "company.contacts"

[[fields]]
name = "company"
value = "company.name"

[[fields]]
name = "website"
value = "company.website"

[[fields]]
name = "abn"
value = "company.abn"

[[fields]]
name = "address"
value = "company.address"

[[fields]]
name = "street"
value = "company.address.street"

[[fields]]
name = "contacts"
value = "company.contacts"

[[fields]]
name = "who"
template = "{company.short|person.name}"
`)
	set, _, err := LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := set.Get("visit")
	g := New(8)
	ps := people(g, 2)
	ps[0].Company = ""
	r, err := g.Record(s, RecordInput{People: ps[:1]}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(r.Data, &doc)
	for _, k := range []string{"company", "website", "abn", "street"} {
		if doc[k] != "" {
			t.Errorf("%s = %v, want empty", k, doc[k])
		}
	}
	if doc["address"] != nil || len(doc["contacts"].([]any)) != 0 || len(r.People) != 0 {
		t.Errorf("address %v, contacts %v, links %v", doc["address"], doc["contacts"], r.People)
	}
	// The first alternative is empty, so the second fills it; for the title's
	// second placeholder that is a generated label.
	if doc["who"] != ps[0].Name() || !strings.HasPrefix(r.Title, ps[0].Name()+" ") || r.Title == ps[0].Name()+" " {
		t.Errorf("who %q, title %q", doc["who"], r.Title)
	}
	// With a company the first alternative is used, and no label is drawn.
	r, _ = g.Record(s, RecordInput{People: ps[1:]}, 0)
	co := CompanyFor(ps[1].Company)
	if r.Title != trimSuffix(co.Name)+" "+co.Name || len(r.People) != 1 {
		t.Errorf("with a company: title %q, links %v", r.Title, r.People)
	}

	// Company records skip individuals, and need someone with a company.
	company, _ := set.Get(SchemaCompany)
	if r, err := g.Record(company, RecordInput{People: ps}, 0); err != nil || !strings.Contains(string(r.Data), co.Name) {
		t.Errorf("company record = %s, %v", r.Data, err)
	}
	if _, err := g.Record(company, RecordInput{People: ps[:1]}, 0); err == nil || !strings.Contains(err.Error(), "work for a company") {
		t.Errorf("company record about an individual err = %v", err)
	}
}

func TestTemplateFallbackValidation(t *testing.T) {
	for body, want := range map[string]string{
		`template = "{company.short|}"`:          "empty alternative",
		`template = "{|person.name}"`:            "empty alternative",
		`template = "{company.short|boss.name}"`: "person.*, company.*",
		`template = "{company.short|nothing}"`:   `"nothing" is not a field`,
	} {
		dir := t.TempDir()
		writeSchema(t, dir, "ticket", minimal+"\n[[fields]]\nname = \"x\"\n"+body+"\n")
		if _, _, err := LoadSchemas(dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}

// TestTaskRecords checks a task is assigned to its person, with known values
// and a due date after it was created, in both profiles.
func TestTaskRecords(t *testing.T) {
	task, _ := BuiltinSchemas().Get(SchemaTask)
	g := New(21)
	ps := people(g, 5)
	statuses := set("todo", "in-progress", "blocked", "in-review", "done")
	priorities := set("low", "normal", "high", "urgent")
	for i := 0; i < 20; i++ {
		profile := Profiles[i%len(Profiles)]
		r, err := g.Record(task, RecordInput{Profile: profile, People: ps}, i)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			TaskNumber string  `json:"task_number"`
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Priority   string  `json:"priority"`
			AssigneeID string  `json:"assignee_person_id"`
			Assignee   string  `json:"assignee"`
			Company    string  `json:"company"`
			Created    string  `json:"created"`
			Due        string  `json:"due"`
			Estimate   float64 `json:"estimate_hours"`
		}
		if err := json.Unmarshal(r.Data, &doc); err != nil {
			t.Fatal(err)
		}
		p := ps[i%len(ps)]
		if doc.AssigneeID != p.ID || doc.Assignee != p.Name() || doc.Company != p.Company || len(r.People) != 1 || r.People[0] != (PersonRole{p.ID, "assignee"}) {
			t.Errorf("task %d is not %s's: %+v, links %v", i, p.ID, doc, r.People)
		}
		if !strings.HasPrefix(doc.TaskNumber, "TSK-") || doc.Name == "" || r.Title != doc.TaskNumber+" "+doc.Name {
			t.Errorf("task %d: number %q, name %q, title %q", i, doc.TaskNumber, doc.Name, r.Title)
		}
		if !statuses[doc.Status] || !priorities[doc.Priority] || doc.Estimate < 1 || doc.Estimate > 40 || doc.Due <= doc.Created {
			t.Errorf("task %d: %s, %s, %v hours, created %s, due %s", i, doc.Status, doc.Priority, doc.Estimate, doc.Created, doc.Due)
		}
	}
}
