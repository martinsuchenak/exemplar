package synth

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/build"
)

// mustParse parses a schema file sent with a request (no file name).
func mustParse(t *testing.T, body string) *Schema {
	t.Helper()
	sc, err := parseSchema("", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// decode generates the index-th record and decodes its JSON.
func decode(t *testing.T, sc *Schema, in RecordInput, index int) map[string]any {
	t.Helper()
	r, err := New(7).Record(sc, in, index)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(r.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Keys in any case, true or false, fixed values of any type, values picked
// from tables and read key by key, hidden helpers, numbers as text, and
// the record's number.
func TestValueTypes(t *testing.T) {
	sc := mustParse(t, `name = "crm-thing"
exemplar = "0.1"
title = "{companyName} #{record.number}"

[[fields]]
name = "status"
pick = [{ id = 3, name = "Won" }]
hidden = true

[[fields]]
name = "idStatus"
value = "status.id"

[[fields]]
name = "statusName"
template = "{status.name}"

[[fields]]
name = "companyName"
value = "company.name"

[[fields]]
name = "isActive"
bool = 1

[[fields]]
name = "isArchived"
bool = 0

[[fields]]
name = "settings"
const = { theme = "dark", sizes = [1, 2], on = true }

[[fields]]
name = "branch"
const = 4

[[fields]]
name = "code"
int = [42, 42]
format = "string"

[[fields]]
name = "price"
money = [12, 12]
format = "string"

[[fields]]
name = "number"
value = "record.number"

[[fields]]
name = "neverThere"
int = [1, 1]
optional = 1
`)
	if strings.Join(sc.Fields, ",") != "idStatus,statusName,companyName,isActive,isArchived,settings,branch,code,price,number,neverThere" {
		t.Errorf("fields = %v", sc.Fields)
	}
	if sc.Exemplar != "0.1" {
		t.Errorf("exemplar = %q", sc.Exemplar)
	}
	g := New(1)
	in := RecordInput{People: people(g, 2)}
	got := decode(t, sc, in, 2) // record 3 is about person 3 % 2, the first

	want := map[string]any{
		"schema": "crm-thing", "idStatus": 3.0, "statusName": "Won", "companyName": in.People[0].Company,
		"isActive": true, "isArchived": false, "settings": map[string]any{"theme": "dark", "sizes": []any{1.0, 2.0}, "on": true},
		"branch": 4.0, "code": "42", "price": "12.00", "number": 3.0, "neverThere": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("record =\n%v\nwant\n%v", got, want)
	}
	r, _ := New(7).Record(sc, in, 2)
	if !strings.HasSuffix(r.Title, " #3") {
		t.Errorf("title = %q", r.Title)
	}
}

// An optional field is null about as often as its chance says, and a bool
// true as often as its chance; the same seed gives the same record.
func TestChances(t *testing.T) {
	sc := mustParse(t, `name = "chance"
title = "x"

[[fields]]
name = "maybe"
int = [1, 1]
optional = 0.25

[[fields]]
name = "flag"
bool = 0.75
`)
	in := RecordInput{People: people(New(1), 1)}
	g := New(9)
	nulls, trues := 0, 0
	for i := range 4000 {
		r, err := g.Record(sc, in, i)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(r.Data, &m)
		if m["maybe"] == nil {
			nulls++
		}
		if m["flag"] == true {
			trues++
		}
	}
	if nulls < 850 || nulls > 1150 || trues < 2850 || trues > 3150 {
		t.Errorf("nulls %d, trues %d of 4000", nulls, trues)
	}
	a, _ := New(3).Record(sc, in, 0)
	b, _ := New(3).Record(sc, in, 0)
	if string(a.Data) != string(b.Data) {
		t.Error("the same seed gave different records")
	}
}

const bundle = `name = "account"
step = "company"
title = "{name}"

[[fields]]
name = "name"
value = "company.name"

[[fields]]
name = "address"
object = true

  [[fields.fields]]
  name = "street"
  value = "company.address.street"

  [[fields.fields]]
  name = "owner"
  template = "{name}"

[[fields]]
name = "contacts"
list = { each = "company.contacts" }

  [[fields.fields]]
  name = "firstName"
  value = "person.first_name"

  [[fields.fields]]
  name = "email"
  value = "person.email"

  [[fields.fields]]
  name = "position"
  value = "item.number"

[[fields]]
name = "deals"
list = { count = [2, 2] }

  [[fields.fields]]
  name = "name"
  template = "Deal {item.number} for {name}"

  [[fields.fields]]
  name = "value"
  money = [100, 100]

  [[fields.fields]]
  name = "secret"
  money = [1, 1]
  hidden = true

  [[fields.fields]]
  name = "tasks"
  list = { count = [1, 1] }

    [[fields.fields.fields]]
    name = "title"
    template = "Follow up {name}"

[[fields]]
name = "pipeline"
sum = "deals.value"
format = "number"

[[fields]]
name = "hiddenTotal"
sum = "deals.secret"
format = "number"
`

// An object nests fields; a list makes an item per person at the company,
// or a count of them; items read the fields above them, their own hiding
// the outer ones, and nest further; a sum adds up a list's money column.
func TestObjectsAndLists(t *testing.T) {
	sc := mustParse(t, bundle)
	g := New(1)
	ps := people(g, 4) // two companies of two
	got := decode(t, sc, RecordInput{People: ps}, 0)
	company := ps[0].Company
	if got["name"] != company {
		t.Fatalf("name = %v", got["name"])
	}
	addr := got["address"].(map[string]any)
	if addr["owner"] != company || addr["street"] != CompanyFor(company).Address.Street {
		t.Errorf("address = %v", addr)
	}
	contacts := got["contacts"].([]any)
	var at []Person
	for _, p := range ps {
		if p.Company == company {
			at = append(at, p)
		}
	}
	if len(contacts) != len(at) {
		t.Fatalf("contacts = %v", contacts)
	}
	for i, c := range contacts {
		m := c.(map[string]any)
		if m["firstName"] != at[i].FirstName || m["email"] != at[i].Email || m["position"] != float64(i+1) {
			t.Errorf("contact %d = %v", i, m)
		}
	}
	deals := got["deals"].([]any)
	if len(deals) != 2 {
		t.Fatalf("deals = %v", deals)
	}
	for i, d := range deals {
		m := d.(map[string]any)
		if m["name"] != fmt.Sprintf("Deal %d for %s", i+1, company) || m["value"] != "$100.00" || m["secret"] != nil {
			t.Errorf("deal %d = %v", i, m)
		}
		if _, ok := m["secret"]; ok {
			t.Errorf("deal %d shows a hidden field", i)
		}
		tasks := m["tasks"].([]any)
		if len(tasks) != 1 || tasks[0].(map[string]any)["title"] != fmt.Sprintf("Follow up Deal %d for %s", i+1, company) {
			t.Errorf("deal %d tasks = %v", i, tasks)
		}
	}
	if got["pipeline"] != 200.0 || got["hiddenTotal"] != 2.0 {
		t.Errorf("pipeline %v, hidden total %v", got["pipeline"], got["hiddenTotal"])
	}
}

// A list of the company's people is empty for a person with none, and a
// record cannot grow without bound.
func TestListLimits(t *testing.T) {
	sc := mustParse(t, `name = "solo"
title = "x"

[[fields]]
name = "contacts"
list = { each = "company.contacts" }

  [[fields.fields]]
  name = "id"
  value = "person.id"
`)
	p := people(New(1), 1)
	p[0].Company = ""
	if got := decode(t, sc, RecordInput{People: p}, 0); !reflect.DeepEqual(got["contacts"], []any{}) {
		t.Errorf("contacts = %v", got["contacts"])
	}

	big := mustParse(t, `name = "big"
title = "x"

[[fields]]
name = "a"
list = { count = [100, 100] }

  [[fields.fields]]
  name = "b"
  list = { count = [100, 100] }

    [[fields.fields.fields]]
    name = "c"
    list = { count = [100, 100] }

      [[fields.fields.fields.fields]]
      name = "d"
      int = [1, 1]
`)
	if _, err := New(1).Record(big, RecordInput{People: p}, 0); err == nil || !strings.Contains(err.Error(), "at most 100000 values") {
		t.Errorf("err = %v", err)
	}
}

// Each mistake in the new field types is refused when the file is read.
func TestNestedValidation(t *testing.T) {
	head := "name = \"x\"\ntitle = \"x\"\n"
	deep := head
	prefix := "fields"
	for range 5 {
		deep += "\n[[" + prefix + "]]\nname = \"n\"\nobject = true\n"
		prefix += ".fields"
	}
	deep += "\n[[" + prefix + "]]\nname = \"v\"\nint = [1, 1]\n"
	for _, c := range []struct{ name, body, want string }{
		{"fields without object", head + "[[fields]]\nname = \"a\"\nint = [1, 1]\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n", "only for object and list"},
		{"object false", head + "[[fields]]\nname = \"a\"\nobject = false\n", "object takes true"},
		{"empty object", head + "[[fields]]\nname = \"a\"\nobject = true\n", "needs its own [[fields]]"},
		{"count and each", head + "[[fields]]\nname = \"a\"\nlist = { count = [1, 2], each = \"company.contacts\" }\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n", "not both"},
		{"bad each", head + "[[fields]]\nname = \"a\"\nlist = { each = \"people\" }\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n", "company.contacts"},
		{"too many items", head + "[[fields]]\nname = \"a\"\nlist = { count = [1, 101] }\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n", "within 0-100"},
		{"too deep", deep, "nest at most 4 deep"},
		{"unknown nested key", head + "[[fields]]\nname = \"a\"\nobject = true\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\ncolour = \"red\"\n", `unknown key "fields.fields.colour"`},
		{"object key", head + "[[fields]]\nname = \"a\"\nobject = true\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n[[fields]]\nname = \"c\"\nvalue = \"a.z\"\n", "object a has no field"},
		{"hidden object key", head + "[[fields]]\nname = \"a\"\nobject = true\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\nhidden = true\n[[fields]]\nname = \"c\"\nvalue = \"a.b\"\n", "object a has no field"},
		{"const key", head + "[[fields]]\nname = \"a\"\nconst = { id = 1 }\n[[fields]]\nname = \"c\"\nvalue = \"a.name\"\n", "const a has no key name"},
		{"pick key", head + "[[fields]]\nname = \"a\"\npick = [{ id = 1 }, { name = \"x\" }]\n[[fields]]\nname = \"c\"\nvalue = \"a.id\"\n", "an item of pick a has no key id"},
		{"list key", head + "[[fields]]\nname = \"a\"\nlist = { count = [1, 1] }\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n[[fields]]\nname = \"c\"\nvalue = \"a.b\"\n", "holds no keys"},
		{"item outside a list", head + "[[fields]]\nname = \"a\"\nvalue = \"item.number\"\n", "only for a list's items"},
		{"record attribute", head + "[[fields]]\nname = \"a\"\nvalue = \"record.id\"\n", "record.number"},
		{"bool range", head + "[[fields]]\nname = \"a\"\nbool = 2\n", "chance from 0 to 1"},
		{"bool word", head + "[[fields]]\nname = \"a\"\nbool = \"yes\"\n", "chance from 0 to 1"},
		{"optional zero", head + "[[fields]]\nname = \"a\"\nint = [1, 1]\noptional = 0\n", "more than 0"},
		{"optional sum", head + "[[fields]]\nname = \"a\"\nmoney = [1, 1]\noptional = 0.5\n[[fields]]\nname = \"b\"\nsum = \"a\"\n", "optional field can be null"},
		{"optional after", head + "[[fields]]\nname = \"a\"\ndate = { within_days = 5 }\noptional = 0.5\n[[fields]]\nname = \"b\"\ndate = { after = \"a\", days = [1, 2] }\n", "optional field can be null"},
		{"optional percent", head + "[[fields]]\nname = \"a\"\nmoney = [1, 1]\noptional = 0.5\n[[fields]]\nname = \"b\"\npercent = { of = \"a\", rate = 10 }\n", "optional field can be null"},
		{"list sum column", head + "[[fields]]\nname = \"a\"\nlist = { count = [1, 1] }\n[[fields.fields]]\nname = \"b\"\nint = [1, 1]\n[[fields]]\nname = \"c\"\nsum = \"a.b\"\n", "no money field"},
		{"const date", head + "[[fields]]\nname = \"a\"\nconst = 2026-01-02\n", "write a date as a string"},
		{"const nan", head + "[[fields]]\nname = \"a\"\nconst = nan\n", "nan and inf"},
		{"pick inf", head + "[[fields]]\nname = \"a\"\npick = [1, inf]\n", "nan and inf"},
		{"list nan", head + "[[fields]]\nname = \"a\"\npick = \"@n\"\n[lists.all]\nn = [{ x = -inf }]\n", "nan and inf"},
		{"optional list sum", head + "[[fields]]\nname = \"a\"\nlist = { count = [1, 1] }\noptional = 0.5\n[[fields.fields]]\nname = \"b\"\nmoney = [1, 1]\n[[fields]]\nname = \"c\"\nsum = \"a.b\"\n", "optional field can be null"},
		{"int format", head + "[[fields]]\nname = \"a\"\nint = [1, 1]\nformat = \"hex\"\n", "can only be string"},
		{"request list in file", head + "request_lists = [\"users\"]\n[lists.all]\nusers = [\"a\"]\n", "leave it out of request_lists"},
		{"request list name", head + "request_lists = [\"bad name\"]\n", "letters, digits"},
		{"version", head + "exemplar = \"latest\"\n", "oldest version"},
		{"catalogue list", "name = \"cat\"\nstep = \"catalogue\"\ntitle = \"x\"\n[catalogue]\nsize = 1\n[[catalogue.fields]]\nname = \"a\"\nint = [1, 1]\n[[fields]]\nname = \"b\"\nlist = { count = [1, 1] }\n[[fields.fields]]\nname = \"c\"\nint = [1, 1]\n", "catalogue records cannot have lists"},
		{"entry object", "name = \"cat\"\nstep = \"catalogue\"\ntitle = \"x\"\n[catalogue]\nsize = 1\n[[catalogue.fields]]\nname = \"a\"\nobject = true\n[[catalogue.fields.fields]]\nname = \"c\"\nint = [1, 1]\n", "cannot have objects or lists"},
		{"entry optional", "name = \"cat\"\nstep = \"catalogue\"\ntitle = \"x\"\n[catalogue]\nsize = 1\n[[catalogue.fields]]\nname = \"a\"\nint = [1, 1]\noptional = 0.5\n", "cannot be optional"},
		{"entry record number", "name = \"cat\"\nstep = \"catalogue\"\ntitle = \"x\"\n[catalogue]\nsize = 1\n[[catalogue.fields]]\nname = \"a\"\nvalue = \"record.number\"\n", "no record number"},
		{"entry request list", "name = \"cat\"\nstep = \"catalogue\"\ntitle = \"x\"\nrequest_lists = [\"u\"]\n[catalogue]\nsize = 1\n[[catalogue.fields]]\nname = \"a\"\npick = \"@u\"\n", "catalogue entries cannot use one"},
		{"missing list", head + "[[fields]]\nname = \"a\"\npick = \"@users\"\n", "name it in request_lists"},
	} {
		if _, err := parseSchema("", []byte(c.body)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	// A name inside an object may repeat one outside it; twice inside it
	// may not.
	ok := head + "[[fields]]\nname = \"id\"\nint = [1, 1]\n[[fields]]\nname = \"a\"\nobject = true\n[[fields.fields]]\nname = \"id\"\nint = [2, 2]\n"
	if _, err := parseSchema("", []byte(ok)); err != nil {
		t.Errorf("shadowing: %v", err)
	}
	if _, err := parseSchema("", []byte(ok+"[[fields.fields]]\nname = \"id\"\nint = [3, 3]\n")); err == nil || !strings.Contains(err.Error(), "defined twice") {
		t.Errorf("twice in an object: %v", err)
	}
}

// A request's lists replace the file's and fill the ones it says a request
// gives; a key missing from a request's item fails the record, naming it.
func TestRequestLists(t *testing.T) {
	sc := mustParse(t, `name = "deal"
title = "x"
request_lists = ["owners"]

[[fields]]
name = "stage"
pick = "@stages"

[[fields]]
name = "owner"
pick = "@owners"
hidden = true

[[fields]]
name = "idOwner"
value = "owner.id"

[lists.all]
stages = ["lead"]
`)
	if strings.Join(sc.Lists, ",") != "owners,stages" || strings.Join(sc.RequestLists, ",") != "owners" {
		t.Errorf("lists %v, request lists %v", sc.Lists, sc.RequestLists)
	}
	in := RecordInput{People: people(New(1), 1), Lists: map[string][]any{"owners": {map[string]any{"id": 17.0}}, "stages": {"won"}}}
	got := decode(t, sc, in, 0)
	if got["stage"] != "won" || got["idOwner"] != 17.0 {
		t.Errorf("record = %v", got)
	}
	in.Lists["owners"] = []any{map[string]any{"name": "x"}}
	if _, err := New(1).Record(sc, in, 0); err == nil || !strings.Contains(err.Error(), `owner has no key id`) {
		t.Errorf("err = %v", err)
	}
	delete(in.Lists, "owners")
	if _, err := New(1).Record(sc, in, 0); err == nil || !strings.Contains(err.Error(), `list "owners" is not given`) {
		t.Errorf("err = %v", err)
	}

	many := map[string][]any{}
	for i := range MaxRequestLists + 1 {
		many[fmt.Sprint("l", i)] = []any{"x"}
	}
	for _, c := range []struct {
		lists map[string][]any
		want  string
	}{
		{map[string][]any{"owners": {"a"}}, ""},
		{map[string][]any{}, `needs list "owners"`},
		{map[string][]any{"owners": {"a"}, "colours": {"red"}}, `"colours": no schema`},
		{map[string][]any{"owners": {}}, "is empty"},
		{map[string][]any{"owners": make([]any, MaxRequestListItems+1)}, "over 10000 items"},
		{many, "at most 100 lists"},
	} {
		err := CheckRequestLists([]*Schema{sc}, c.lists)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("lists %d: err = %v, want %q", len(c.lists), err, c.want)
		}
	}
}

// Files sent with a request join the server's schemas for that request
// only, may use the server's catalogues, and may not take a name.
func TestWithRequestSchemas(t *testing.T) {
	base := BuiltinSchemas()
	order := `name = "order"
title = "{total}"

[[fields]]
name = "items"
lines = { catalogue = "product", count = [1, 2], qty = [1, 1] }

[[fields]]
name = "total"
sum = "items.amount"
`
	set, names, err := WithRequestSchemas(base, []string{minimal, order})
	if err != nil || len(names) != 2 || names[0] != "ticket" || names[1] != "order" {
		t.Fatalf("names %v, %v", names, err)
	}
	if _, ok := base.Get("ticket"); ok {
		t.Error("a request's schema reached the server's set")
	}
	sc, _ := set.Get("order")
	if r, err := New(1).Record(sc, RecordInput{People: people(New(1), 1)}, 0); err != nil || !strings.Contains(string(r.Data), `"total": "$`) {
		t.Errorf("order = %s, %v", r.Data, err)
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{strings.Replace(minimal, `"ticket"`, `"quote"`, 1)}, `"quote" is taken`},
		{[]string{minimal, minimal}, `"ticket" is taken`},
		{[]string{"name = "}, "schema file 1"},
		{[]string{strings.Replace(order, `"product"`, `"nothing"`, 1)}, "not a catalogue schema"},
		{make([]string, MaxRequestSchemas+1), "at most 20"},
		{[]string{minimal + "\n#" + strings.Repeat("x", MaxRequestSchemaBytes)}, "over 262144 bytes"},
	} {
		if _, _, err := WithRequestSchemas(base, c.files); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

// The version a file needs is checked before anything else in it, so a
// file for a newer release says so rather than naming a key this one does
// not know.
func TestVersionComesFirst(t *testing.T) {
	old := build.Version
	t.Cleanup(func() { build.Version = old })
	build.Version = "v0.5.0"
	_, err := parseSchema("", []byte("name = \"x\"\nexemplar = \"9.0\"\ntitle = \"x\"\nnewthing = { shape = 1 }\n[[fields]]\nname = \"a\"\nint = \"new\"\n"))
	if err == nil || !strings.Contains(err.Error(), "needs exemplar 9.0 or later; this is v0.5.0") {
		t.Errorf("err = %v", err)
	}
	if _, err := parseSchema("", []byte("exemplar = 5\n")); err == nil {
		t.Error("a number for exemplar was taken")
	}
}

// A template reads an object or list field as its JSON.
func TestTemplateReadsAnObject(t *testing.T) {
	sc := mustParse(t, `name = "x"
title = "x"

[[fields]]
name = "box"
object = true

  [[fields.fields]]
  name = "n"
  int = [3, 3]

[[fields]]
name = "label"
template = "box={box}"
`)
	if got := decode(t, sc, RecordInput{People: people(New(1), 1)}, 0); got["label"] != `box={"n":3}` {
		t.Errorf("label = %v", got["label"])
	}
}

// A schema names the oldest exemplar it works with; only a release build
// compares, since a dev build may hold newer features.
func TestCheckExemplar(t *testing.T) {
	for _, c := range []struct {
		need, have string
		ok         bool
	}{
		{"", "v0.1.0", true},
		{"0.5", "v0.5.0", true},
		{"0.5", "0.5.3", true},
		{"0.5", "v1.0.0", true},
		{"0.5.2", "v0.5.1", false},
		{"0.5", "v0.4.9", false},
		{"1.0", "0.9.9", false},
		{"0.5", "dev", true},
		{"0.5", "v0.4.0-3-gabc1234", true},
		{"0.5", "v0.5.0-rc1", true},
		{"five", "v0.5.0", false},
		{"0.5.x", "dev", false},
	} {
		if err := checkExemplar(c.need, c.have); (err == nil) != c.ok {
			t.Errorf("need %q, have %q: %v", c.need, c.have, err)
		}
	}
}

// A catalogue entry's hidden field is left out of its record and of the
// lines that use it, but read by the fields below it.
func TestHiddenEntryFields(t *testing.T) {
	cat := `name = "part"
step = "catalogue"
title = "{name}"

[catalogue]
size = 2

[[catalogue.fields]]
name = "group"
pick = [{ id = 9 }]
hidden = true

[[catalogue.fields]]
name = "name"
template = "Part"

[[catalogue.fields]]
name = "unit_price"
money = [5, 5]
hidden = true

[[fields]]
name = "idGroup"
value = "group.id"
`
	order := `name = "order"
title = "x"

[[fields]]
name = "items"
lines = { catalogue = "part", count = [1, 1], qty = [2, 2] }

[[fields]]
name = "total"
sum = "items.amount"
`
	set, names, err := WithRequestSchemas(BuiltinSchemas(), []string{cat, order})
	if err != nil {
		t.Fatal(err)
	}
	part, _ := set.Get(names[0])
	if got := decode(t, part, RecordInput{}, 0); !reflect.DeepEqual(got, map[string]any{"schema": "part", "name": "Part", "idGroup": 9.0}) {
		t.Errorf("part = %v", got)
	}
	o, _ := set.Get(names[1])
	got := decode(t, o, RecordInput{People: people(New(1), 1)}, 0)
	line := got["items"].([]any)[0].(map[string]any)
	if _, ok := line["group"]; ok || line["amount"] != "$10.00" || got["total"] != "$10.00" {
		t.Errorf("order = %v", got)
	}
	if strings.Join(part.Fields, ",") != "name,idGroup" {
		t.Errorf("fields = %v", part.Fields)
	}
}

// The worked example in the docs loads and makes what it says: an account
// per company with its contacts, opportunities, and projects with requests.
func TestDocsExample(t *testing.T) {
	data, err := os.ReadFile("../../docs/examples/crm-account.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../docs/examples/crm-lists.json")
	if err != nil {
		t.Fatal(err)
	}
	var lists map[string][]any
	if err := json.Unmarshal(raw, &lists); err != nil {
		t.Fatal(err)
	}
	set, names, err := WithRequestSchemas(BuiltinSchemas(), []string{string(data)})
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := set.Get(names[0])
	if err := CheckRequestLists([]*Schema{sc}, lists); err != nil {
		t.Fatal(err)
	}
	g := New(42)
	in := RecordInput{People: g.People(30), Lists: lists}
	for i := range in.People {
		in.People[i].ID = g.ID()
	}
	projects, requests := 0, 0
	for i := range 10 {
		r, err := g.Record(sc, in, i)
		if err != nil {
			t.Fatal(err)
		}
		var acc map[string]any
		if err := json.Unmarshal(r.Data, &acc); err != nil {
			t.Fatal(err)
		}
		if len(acc["contacts"].([]any)) == 0 || acc["idUserSalesPerson"] == nil || acc["address"].(map[string]any)["street"] == "" {
			t.Errorf("account = %s", r.Data)
		}
		for _, o := range acc["opportunities"].([]any) {
			if p, ok := o.(map[string]any)["project"].(map[string]any); ok {
				projects++
				requests += len(p["requests"].([]any))
			}
		}
	}
	if projects == 0 || requests == 0 {
		t.Errorf("%d projects, %d requests", projects, requests)
	}
}
