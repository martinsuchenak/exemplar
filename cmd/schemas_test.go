package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/recordschema"
	"github.com/martinsuchenak/exemplar/internal/synth"
)

func TestSchemasCmdExists(t *testing.T) {
	cmd := schemasCmd()
	if cmd == nil {
		t.Fatal("schemasCmd() should not return nil")
	}
	if cmd.Name != "schemas" {
		t.Errorf("expected command name %q, got %q", "schemas", cmd.Name)
	}
}

func TestSchemasList(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	out, err := s.run(t, member, "schemas", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "REQUEST LISTS", "quote", "catalogue", "built-in", "quote_number, name, company"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	var schemas []synth.Schema
	s.runJSON(t, member, &schemas, "schemas", "list")
	if len(schemas) != len(synth.BuiltinSchemas().Names()) || schemas[0].Name != synth.SchemaAddress {
		t.Errorf("json = %+v", schemas)
	}
}

const cliTicket = "name = \"ticket\"\ntitle = \"{reference}\"\n\n[[fields]]\nname = \"reference\"\nref = \"TK\"\n"

// An admin adds a schema from a file, saves it with show, edits and sends
// it back with update, and removes it; members can show it, not change it.
func TestSchemasAddShowUpdateRemove(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	file := writeFile(t, "ticket.toml", cliTicket)
	if _, err := s.run(t, member, "schemas", "add", file); err == nil || !strings.Contains(err.Error(), "admin key") {
		t.Errorf("member add: %v", err)
	}
	out, err := s.run(t, s.adminKey, "schemas", "add", file)
	if err != nil || !strings.Contains(out, "Added record schema ticket: version 1, hash ") {
		t.Fatalf("add = %q, %v", out, err)
	}
	if out, _ := s.run(t, member, "schemas", "list"); !strings.Contains(out, "api v1") || !strings.Contains(out, "ticket") {
		t.Errorf("list:\n%s", out)
	}

	show, errOut, err := runCLIWith(t, "", "schemas", "show", "ticket", "--server", s.url, "--api-key", member)
	if err != nil || show != cliTicket || !strings.Contains(errOut, "ticket: api v1 schema, hash ") {
		t.Errorf("show = %q, %q, %v", show, errOut, err)
	}
	if out, err := s.run(t, member, "schemas", "show", "ticket", "--api"); err != nil || out != cliTicket {
		t.Errorf("show --api = %q, %v", out, err)
	}
	var d recordschema.Definition
	s.runJSON(t, member, &d, "schemas", "show", "quote")
	if d.Source != synth.SourceBuiltin || !strings.Contains(d.TOML, `name = "quote"`) {
		t.Errorf("show quote = %+v", d)
	}

	edited := writeFile(t, "edited.toml", strings.Replace(show, "TK", "SR", 1))
	if _, err := s.run(t, s.adminKey, "schemas", "update", edited, "--if-version", "4"); err == nil || !strings.Contains(err.Error(), "at version 1, not 4") {
		t.Errorf("stale update: %v", err)
	}
	if out, err := s.run(t, s.adminKey, "schemas", "update", edited, "--if-version", "1"); err != nil || !strings.Contains(out, "Updated record schema ticket: version 2") {
		t.Errorf("update = %q, %v", out, err)
	}
	if _, err := s.run(t, s.adminKey, "schemas", "update", writeFile(t, "bad.toml", "name = 1\n")); err == nil || !strings.Contains(err.Error(), "bad.toml") {
		t.Errorf("update with no name: %v", err)
	}
	if _, err := s.run(t, s.adminKey, "schemas", "add", filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("add of a missing file succeeded")
	}

	if out, err := s.run(t, s.adminKey, "schemas", "remove", "ticket"); err != nil || !strings.Contains(out, "Removed record schema ticket.") {
		t.Errorf("remove = %q, %v", out, err)
	}
	if _, err := s.run(t, member, "schemas", "show", "ticket"); err == nil {
		t.Error("show after remove succeeded")
	}
}

// The SOURCE column names where a schema came from, also from a server
// that reports only whether it is built in.
func TestSchemaSource(t *testing.T) {
	for _, c := range []struct {
		source  string
		builtin bool
		version int
		want    string
	}{
		{synth.SourceAPI, false, 3, "api v3"},
		{synth.SourceFile, false, 0, "file"},
		{"", true, 0, "built-in"},
		{"", false, 0, "file"},
	} {
		if got := schemaSource(c.source, c.builtin, c.version); got != c.want {
			t.Errorf("%+v = %q", c, got)
		}
	}
}

// A schema's row in schemas list, including one that does not load.
func TestSchemaRow(t *testing.T) {
	ok := synth.Schema{Name: "ticket", Step: "person", Source: synth.SourceAPI, Version: 2, Hash: "1a2b3c4d", Fields: []string{"reference", "subject"}, RequestLists: []string{"users"}}
	if got := strings.Join(schemaRow(ok), "|"); got != "ticket|person|api v2|1a2b3c4d|users|reference, subject" {
		t.Errorf("row = %s", got)
	}
	broken := synth.Schema{Name: "part", Source: synth.SourceAPI, Version: 1, Fields: []string{}, Problem: `"part" is taken by a file schema; give it another name`}
	if got := strings.Join(schemaRow(broken), "|"); got != `part|-|api v1|-|-|NOT LOADED: "part" is taken by a file schema; give it another name` {
		t.Errorf("row = %s", got)
	}
}
