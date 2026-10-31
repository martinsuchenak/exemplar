package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/fixture"
	"github.com/martinsuchenak/exemplar/internal/generate"
)

func TestGenerateCmdExists(t *testing.T) {
	cmd := generateCmd()
	if cmd == nil {
		t.Fatal("generateCmd() should not return nil")
	}
	if cmd.Name != "generate" {
		t.Errorf("expected command name %q, got %q", "generate", cmd.Name)
	}
}

func TestGenerateCommand(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "gen")

	out, err := s.run(t, member, "generate", d.ID, "--kind", "files", "--count", "2", "--formats", "pdf,docx", "--seed", "21", "--people", "2", "--profile", "manufacturing", "--scenario", "smoke", "--tags", "a,b")
	if err != nil || !strings.Contains(out, ".pdf") || !strings.Contains(out, ".docx") || !strings.Contains(out, "with seed 21") {
		t.Fatalf("files = %q, %v", out, err)
	}
	var res generate.Result
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "emails", "--count", "2", "--shapes", "reply,bounce", "--seed", "22")
	if res.Seed != 22 || len(res.Fixtures) != 2 {
		t.Errorf("emails = %+v", res)
	}
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "records", "--count", "2", "--schemas", "contact,project", "--seed", "23")
	if len(res.Fixtures) != 2 || res.Fixtures[0].RecordSchema != "contact" {
		t.Errorf("records = %+v", res.Fixtures)
	}

	var contacts []fixture.Fixture
	s.runJSON(t, member, &contacts, "fixtures", "list", d.ID, "--record-schema", "contact", "--person", res.PersonIDs[0])
	if len(contacts) != 1 {
		t.Errorf("fixtures for person = %+v", contacts)
	}

	for _, args := range [][]string{
		{"generate", d.ID, "--kind", "videos"},
		{"generate", d.ID, "--kind", "files", "--seed", "abc"},
		{"generate", d.ID, "--kind", "files", "--seed", "0"},
		{"generate", d.ID, "--kind", "files", "--count", "0"},
	} {
		if _, err := s.run(t, member, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestParseSeed(t *testing.T) {
	if v, err := parseSeed(""); err != nil || v != 0 {
		t.Errorf("empty = %d, %v", v, err)
	}
	if v, err := parseSeed("18446744073709551615"); err != nil || v != 1<<64-1 {
		t.Errorf("max = %d, %v", v, err)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if _, err := parseSeed(bad); err == nil {
			t.Errorf("parseSeed(%q) accepted", bad)
		}
	}
}

func TestGenerateFromTemplates(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "mine")
	var base generate.Result
	s.runJSON(t, member, &base, "generate", d.ID, "--kind", "emails", "--count", "1", "--shapes", "reply", "--seed", "5")

	var res generate.Result
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "emails", "--count", "3", "--from", base.Fixtures[0].ID, "--seed", "9")
	if len(res.Fixtures) != 3 {
		t.Fatalf("generated %d", len(res.Fixtures))
	}
	for _, f := range res.Fixtures {
		if f.Generator != "template:"+base.Fixtures[0].ID+" seed=9" || f.Tags[len(f.Tags)-1] != "template" {
			t.Errorf("fixture = %+v", f)
		}
	}
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "emails", "--from", base.Fixtures[0].ID, "--shapes", "plain"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("shapes and from err = %v", err)
	}
}

// dealSchema is a schema file sent with a request: an owner from a list the
// request gives, kept by its exact ID.
const dealSchema = `name = "deal"
exemplar = "0.1"
title = "{dealName}"
request_lists = ["owners"]

[[fields]]
name = "dealName"
template = "{label} for {company.short|person.name}"

[[fields]]
name = "owner"
pick = "@owners"
hidden = true

[[fields]]
name = "idOwner"
value = "owner.id"

[[fields]]
name = "contacts"
list = { each = "company.contacts" }

  [[fields.fields]]
  name = "email"
  value = "person.email"
`

// writeFile writes a test file and returns its path.
func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --schema-file and --lists go with the request to a server, which makes
// records from them in the dataset.
func TestGenerateWithSchemaFiles(t *testing.T) {
	s := startServer(t)
	member := s.member(t, "alice")
	var d dataset.Dataset
	s.runJSON(t, member, &d, "datasets", "create", "--name", "deals")
	schema := writeFile(t, "deal.toml", dealSchema)
	lists := writeFile(t, "lists.json", `{"owners": [{"id": 7, "name": "Sam"}]}`)
	var res generate.Result
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "records", "--count", "2", "--schema-file", schema, "--lists", lists, "--seed", "3")
	if len(res.Fixtures) != 2 || res.Fixtures[0].RecordSchema != "deal" {
		t.Errorf("records = %+v", res.Fixtures)
	}
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "records", "--count", "1", "--schema-file", schema); err == nil || !strings.Contains(err.Error(), `needs list "owners"`) {
		t.Errorf("no lists: %v", err)
	}
	// A schemas directory, as a config file may set for serve, is left
	// to the server.
	t.Setenv("RECORD_SCHEMAS_DIR", t.TempDir())
	if _, err := s.run(t, member, "generate", d.ID, "--kind", "records", "--count", "1", "--schemas", "contact"); err != nil {
		t.Errorf("with RECORD_SCHEMAS_DIR set: %v", err)
	}
	t.Setenv("RECORD_SCHEMAS_DIR", "")

	// A list's large IDs reach the stored record digit for digit.
	big := writeFile(t, "big.json", `{"owners": [{"id": 12345678901234567}]}`)
	s.runJSON(t, member, &res, "generate", d.ID, "--kind", "records", "--count", "1", "--schema-file", schema, "--lists", big, "--seed", "4")
	out, err := s.run(t, member, "fixtures", "download", res.Fixtures[0].ID)
	if err != nil || !strings.Contains(out, `"idOwner": 12345678901234567`) {
		t.Errorf("record = %s, %v", out, err)
	}
	if _, err := s.run(t, member, "generate", "--kind", "records", "--count", "1"); err == nil || !strings.Contains(err.Error(), "missing <dataset>") {
		t.Errorf("no dataset: %v", err)
	}
}

// --local prints records as JSON on stdout and the seed on stderr, with no
// server; a list's numbers keep their exact digits.
func TestGenerateLocal(t *testing.T) {
	t.Setenv("RECORD_SCHEMAS_DIR", "")
	schema := writeFile(t, "deal.toml", dealSchema)
	lists := writeFile(t, "lists.json", `{"owners": [{"id": 12345678901234567, "name": "Sam"}]}`)
	args := []string{"generate", "--local", "--kind", "records", "--count", "3", "--people", "4", "--schema-file", schema, "--lists", lists, "--seed", "8"}
	out, errOut, err := runCLIWith(t, "", args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	var res struct {
		Seed    uint64            `json:"seed"`
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Seed != 8 || len(res.Records) != 3 {
		t.Fatalf("stdout = %s, %v", out, err)
	}
	if !strings.Contains(string(res.Records[0]), `"idOwner": 12345678901234567`) || strings.Contains(string(res.Records[0]), `"owner"`) {
		t.Errorf("record = %s", res.Records[0])
	}
	if !strings.Contains(errOut, "Made 3 records with seed 8") {
		t.Errorf("stderr = %q", errOut)
	}
	again, _, _ := runCLIWith(t, "", args...)
	if again != out {
		t.Error("the same seed gave different records")
	}

	// A schema directory adds to the built-ins, as on a server.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deal.toml"), []byte(dealSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := runCLIWith(t, "", "generate", "--local", "--kind", "records", "--count", "1", "--schemas", "deal", "--record-schemas-dir", dir, "--lists", lists); err != nil || !strings.Contains(out, `"schema": "deal"`) {
		t.Errorf("schemas dir: %s, %v", out, err)
	}

	bad := writeFile(t, "bad.json", `["owners"]`)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"generate", "mine", "--local", "--kind", "records"}, "takes no dataset"},
		{[]string{"generate", "--local", "--kind", "records", "--tags", "a"}, "--local stores nothing"},
		{[]string{"generate", "--local", "--kind", "files"}, "only records"},
		{[]string{"generate", "--local", "--kind", "records", "--schema-file", schema}, `needs list "owners"`},
		{[]string{"generate", "--local", "--kind", "records", "--schema-file", schema, "--lists", bad}, "JSON object of lists"},
		{[]string{"generate", "--local", "--kind", "records", "--lists", filepath.Join(dir, "missing.json")}, "no such file"},
		{[]string{"generate", "--local", "--kind", "records", "--schema-file", filepath.Join(dir, "missing.toml")}, "no such file"},
	} {
		if _, _, err := runCLIWith(t, "", c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}
}
