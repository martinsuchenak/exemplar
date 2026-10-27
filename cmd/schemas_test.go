package cmd

import (
	"strings"
	"testing"

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
	for _, want := range []string{"NAME", "quote", "catalogue", "built-in", "quote_number, name, company"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	var schemas []synth.Schema
	s.runJSON(t, member, &schemas, "schemas", "list")
	if len(schemas) != 7 || schemas[len(schemas)-1].Name != synth.SchemaQuote {
		t.Errorf("json = %+v", schemas)
	}
}
