package synth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const storedPart = `name = "part"
step = "catalogue"
title = "{name}"

[catalogue]
size = 2

[[catalogue.fields]]
name = "name"
template = "Part"

[[catalogue.fields]]
name = "unit_price"
money = [5, 5]
`

const storedOrder = `name = "order"
title = "x"

[[fields]]
name = "items"
lines = { catalogue = "part", count = [1, 1], qty = [2, 2] }
`

// API schemas load beside the base ones, may use each other's catalogues,
// and carry their source, version and file.
func TestWithStoredSchemas(t *testing.T) {
	base := BuiltinSchemas()
	set := WithStoredSchemas(base, []StoredSchema{
		{Name: "order", Version: 3, Text: storedOrder},
		{Name: "part", Version: 1, Text: storedPart},
	})
	order, ok := set.Get("order")
	if !ok || order.Source != SourceAPI || order.Version != 3 || order.Text() != storedOrder || len(order.Hash) != 8 || order.Problem != "" {
		t.Fatalf("order = %+v", order)
	}
	got := decode(t, order, RecordInput{People: people(New(1), 1)}, 0)
	if line := got["items"].([]any)[0].(map[string]any); line["amount"] != "$10.00" {
		t.Errorf("order = %v", got)
	}
	if len(set.Names()) != len(base.Names())+2 || len(set.List()) != len(base.Names())+2 {
		t.Errorf("names = %v", set.Names())
	}
	if len(base.Names()) != len(BuiltinSchemas().Names()) {
		t.Error("the base set changed")
	}
	if set.Current() != set {
		t.Error("a set's Current is not itself")
	}
}

// One that fails is left out with its problem, and so is one using its
// catalogue; the rest load.
func TestWithStoredSchemasLeavesOutBroken(t *testing.T) {
	base := BuiltinSchemas()
	ticket := "name = \"ticket\"\ntitle = \"x\"\n\n[[fields]]\nname = \"ref\"\nref = \"TK\"\n"
	brokenPart := strings.Replace(storedPart, `money = [5, 5]`, `int = [5, 5]`, 1)
	set := WithStoredSchemas(base, []StoredSchema{
		{Name: "order", Version: 1, Text: storedOrder},
		{Name: "part", Version: 2, Text: brokenPart},
		{Name: "ticket", Version: 1, Text: ticket},
		{Name: "bad", Version: 1, Text: "name = "},
		{Name: "other", Version: 1, Text: ticket},
		{Name: "quote", Version: 1, Text: strings.Replace(ticket, "ticket", "quote", 1)},
	})
	if _, ok := set.Get("ticket"); !ok {
		t.Error("ticket did not load")
	}
	for name, want := range map[RecordSchema]string{
		"part":  "unit_price must be money",
		"order": `"part" is not a catalogue schema`,
		"bad":   "",
		"other": `the file names "ticket", not "other"`,
		"quote": `"quote" is taken by a built-in schema`,
	} {
		p := set.Problem(name)
		if p == "" || !strings.Contains(p, want) {
			t.Errorf("%s: problem %q, want %q", name, p, want)
		}
		if sc, ok := set.Get(name); ok && sc.Source == SourceAPI {
			t.Errorf("%s loaded", name)
		}
	}
	listed := map[RecordSchema]*Schema{}
	for _, sc := range set.List() {
		if sc.Problem != "" {
			listed[sc.Name] = sc
		}
	}
	if p := listed["part"]; p == nil || p.Source != SourceAPI || p.Version != 2 || p.Text() != brokenPart || p.Fields == nil || len(p.Fields) != 0 {
		t.Errorf("listed part = %+v", p)
	}
	if set.Problem("ticket") != "" || set.Problem("address") != "" {
		t.Error("a loaded schema has a problem")
	}
	if q, _ := set.Get("quote"); q.Source != SourceBuiltin {
		t.Errorf("quote = %+v", q)
	}
}

func TestSchemaSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "part.toml"), []byte(storedPart), 0o600); err != nil {
		t.Fatal(err)
	}
	set, _, err := LoadSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := set.Get("quote"); q.Source != SourceBuiltin || !strings.Contains(q.Text(), `name = "quote"`) {
		t.Errorf("quote = %s", q.Source)
	}
	if p, _ := set.Get("part"); p.Source != SourceFile || p.Text() != storedPart {
		t.Errorf("part = %s", p.Source)
	}
	sent, names, err := WithRequestSchemas(set, []string{storedOrder})
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := sent.Get(names[0]); o.Source != SourceRequest {
		t.Errorf("order = %s", o.Source)
	}
}

func TestParseSchemaName(t *testing.T) {
	if n, err := ParseSchemaName(storedPart); err != nil || n != "part" {
		t.Errorf("name = %q, %v", n, err)
	}
	if _, err := ParseSchemaName("name = \"x\"\n"); err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("err = %v", err)
	}
	if n, err := SchemaFileName("name = \"x\"\nwhatever = 1\n"); err != nil || n != "x" {
		t.Errorf("file name = %q, %v", n, err)
	}
	for _, bad := range []string{"name = ", "name = \"Bad Name\"", "title = \"x\""} {
		if _, err := SchemaFileName(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
