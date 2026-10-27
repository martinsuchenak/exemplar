package synth

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Record schemas are TOML files: the built-in ones in schemas/, embedded,
// plus any directory of files the server is given. A file combines the field
// types in schemaeval.go; it holds no logic of its own. See
// docs/generation.md for the format.

//go:embed schemas/*.toml
var builtinSchemaFiles embed.FS

// Steps say what successive records of a schema go through.
const (
	StepPerson    = "person"
	StepCompany   = "company"
	StepCatalogue = "catalogue"
)

// Schema is one loaded record schema.
type Schema struct {
	Name        RecordSchema `json:"name"`
	Description string       `json:"description"`
	Step        string       `json:"step"`
	// Builtin is false for a schema read from a directory, including one
	// that replaces a built-in.
	Builtin bool `json:"builtin"`
	// Hash identifies the file's content (8 hex digits of its SHA-256), so
	// fixture provenance names the exact definition that made a record.
	Hash string `json:"hash"`
	// Fields are the record's top-level keys besides "schema", in file order.
	Fields []string `json:"fields"`

	file      string // where it was read from, for errors
	title     string
	titleToks []tmplTok
	links     []schemaLink
	fields    []*field
	catalogue *catalogue
	lists     map[string]map[string][]string // profile ("all" for every one) -> list
}

type schemaLink struct {
	role, value string
}

type catalogue struct {
	size   int
	fields []*field
}

// SchemaSet is the record schemas a server knows.
type SchemaSet struct {
	byName map[RecordSchema]*Schema
}

// Get returns the schema called name.
func (s *SchemaSet) Get(name RecordSchema) (*Schema, bool) {
	sc, ok := s.byName[name]
	return sc, ok
}

// Names returns every schema name, sorted.
func (s *SchemaSet) Names() []RecordSchema {
	names := make([]RecordSchema, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// List returns every schema, sorted by name.
func (s *SchemaSet) List() []*Schema {
	out := make([]*Schema, 0, len(s.byName))
	for _, n := range s.Names() {
		out = append(out, s.byName[n])
	}
	return out
}

// BuiltinSchemas returns the built-in schemas. They are checked by the tests,
// so an error here is a broken build.
func BuiltinSchemas() *SchemaSet {
	set, _, err := LoadSchemas("")
	if err != nil {
		panic(err)
	}
	return set
}

// LoadSchemas returns the built-in schemas plus every *.toml file in dir
// (empty for none), and the names of the built-ins a file replaced. Every
// file is validated, and references between files (a quote's lines naming
// the product catalogue) are checked once all are read, so a bad file fails
// here and never during a generate request.
func LoadSchemas(dir string) (*SchemaSet, []string, error) {
	set := &SchemaSet{byName: map[RecordSchema]*Schema{}}
	entries, err := builtinSchemaFiles.ReadDir("schemas")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		data, err := builtinSchemaFiles.ReadFile("schemas/" + e.Name())
		if err != nil {
			return nil, nil, err
		}
		sc, err := parseSchema(e.Name(), data)
		if err != nil {
			return nil, nil, fmt.Errorf("built-in record schema %s: %w", e.Name(), err)
		}
		sc.Builtin, sc.file = true, "built-in "+e.Name()
		set.byName[sc.Name] = sc
	}
	var replaced []string
	if dir != "" {
		files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
		if err != nil {
			return nil, nil, err
		}
		seen := map[RecordSchema]bool{}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, nil, err
			}
			sc, err := parseSchema(filepath.Base(f), data)
			if err != nil {
				return nil, nil, fmt.Errorf("record schema %s: %w", f, err)
			}
			if seen[sc.Name] {
				return nil, nil, fmt.Errorf("record schema %s: %q is defined twice", f, sc.Name)
			}
			seen[sc.Name] = true
			sc.file = f
			if old, ok := set.byName[sc.Name]; ok && old.Builtin {
				replaced = append(replaced, string(sc.Name))
			}
			set.byName[sc.Name] = sc
		}
	}
	for _, sc := range set.List() {
		if err := sc.checkCatalogues(set); err != nil {
			return nil, nil, fmt.Errorf("record schema %s: %w", sc.file, err)
		}
	}
	return set, replaced, nil
}

var schemaNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// schemaFile is a schema file as TOML decodes it.
type schemaFile struct {
	Name        string      `toml:"name"`
	Description string      `toml:"description"`
	Step        string      `toml:"step"`
	Title       string      `toml:"title"`
	Links       []linkDef   `toml:"links"`
	Fields      []fieldDef  `toml:"fields"`
	Catalogue   *catalogDef `toml:"catalogue"`
	// Lists are word lists per profile, or under "all" for every profile.
	Lists map[string]map[string][]string `toml:"lists"`
}

type linkDef struct {
	Role  string `toml:"role"`
	Value string `toml:"value"`
}

type catalogDef struct {
	Size   int        `toml:"size"`
	Fields []fieldDef `toml:"fields"`
}

// parseSchema reads and validates one file. file is its base name, which
// must be the schema name plus ".toml".
func parseSchema(file string, data []byte) (*Schema, error) {
	var def schemaFile
	md, err := toml.Decode(string(data), &def)
	if err != nil {
		return nil, err
	}
	for _, key := range md.Undecoded() {
		// pick, text and sum take more than one shape and are decoded
		// loosely; compileField checks what is inside them.
		if len(key) > 2 && key[len(key)-3] == "fields" && (key[len(key)-2] == "text" || key[len(key)-2] == "pick" || key[len(key)-2] == "sum") {
			continue
		}
		return nil, fmt.Errorf("unknown key %q", key.String())
	}
	if !schemaNameRe.MatchString(def.Name) {
		return nil, fmt.Errorf("name %q must be lower-case letters, digits or '-'", def.Name)
	}
	if file != def.Name+".toml" {
		return nil, fmt.Errorf("file must be named %s.toml after its schema", def.Name)
	}
	sum := sha256.Sum256(data)
	sc := &Schema{
		Name: RecordSchema(def.Name), Description: def.Description, Step: def.Step,
		Hash: hex.EncodeToString(sum[:4]), title: def.Title, lists: def.Lists,
	}
	if sc.Step == "" {
		sc.Step = StepPerson
	}
	if sc.Step != StepPerson && sc.Step != StepCompany && sc.Step != StepCatalogue {
		return nil, fmt.Errorf("step must be person, company or catalogue, not %q", sc.Step)
	}
	if err := sc.checkLists(); err != nil {
		return nil, err
	}
	known := map[string]*field{}
	if sc.Step == StepCatalogue {
		if def.Catalogue == nil || def.Catalogue.Size < 1 || def.Catalogue.Size > 10000 {
			return nil, fmt.Errorf("a catalogue schema needs [catalogue] with a size of 1-10000")
		}
		sc.catalogue = &catalogue{size: def.Catalogue.Size}
		entry := map[string]*field{}
		for i, fd := range def.Catalogue.Fields {
			f, err := compileField(fd, sc, scopeEntry, entry)
			if err != nil {
				return nil, fmt.Errorf("catalogue field %d (%s): %w", i+1, fd.Name, err)
			}
			entry[f.name] = f
			sc.catalogue.fields = append(sc.catalogue.fields, f)
		}
		if p := entry["unit_price"]; p != nil && p.kind != kindMoney {
			return nil, fmt.Errorf("catalogue field unit_price must be money")
		}
		for _, f := range sc.catalogue.fields {
			if f.kind != kindNth {
				continue
			}
			for _, profile := range Profiles {
				if n := len(sc.list(profile, f.listName)); sc.catalogue.size > n {
					return nil, fmt.Errorf("catalogue size %d is more than list %q's %d items for %s, so entries would repeat", sc.catalogue.size, f.listName, n, profile)
				}
			}
		}
		// A product record is its entry: the entry's fields are the record's
		// first fields.
		for _, f := range sc.catalogue.fields {
			known[f.name] = f
		}
	} else if def.Catalogue != nil {
		return nil, fmt.Errorf("[catalogue] is only for step = \"catalogue\"")
	}
	for i, fd := range def.Fields {
		f, err := compileField(fd, sc, scopeRecord, known)
		if err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i+1, fd.Name, err)
		}
		known[f.name] = f
		sc.fields = append(sc.fields, f)
	}
	for _, f := range append(append([]*field{}, sc.catalogueFields()...), sc.fields...) {
		sc.Fields = append(sc.Fields, f.name)
	}
	if strings.TrimSpace(sc.title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if sc.titleToks, err = compileTemplate(sc.title, sc, scopeRecord, known); err != nil {
		return nil, fmt.Errorf("title: %w", err)
	}
	for _, l := range def.Links {
		if l.Role == "" {
			return nil, fmt.Errorf("every link needs a role")
		}
		if sc.Step == StepCatalogue {
			return nil, fmt.Errorf("a catalogue schema's records link no people")
		}
		if l.Value != "person.id" && l.Value != "company.contacts" {
			return nil, fmt.Errorf("link %q: value must be person.id or company.contacts", l.Role)
		}
		sc.links = append(sc.links, schemaLink{role: l.Role, value: l.Value})
	}
	return sc, nil
}

// checkLists checks every list's profile is known and no list is empty.
func (sc *Schema) checkLists() error {
	for profile, lists := range sc.lists {
		if profile != "all" && !Profile(profile).Valid() {
			return fmt.Errorf("lists.%s: unknown profile (use all, %s)", profile, profileNames())
		}
		for name, items := range lists {
			if len(items) == 0 {
				return fmt.Errorf("lists.%s.%s is empty", profile, name)
			}
		}
	}
	return nil
}

// list returns the named list for profile.
func (sc *Schema) list(profile Profile, name string) []string {
	if l, ok := sc.lists[string(profile)][name]; ok {
		return l
	}
	return sc.lists["all"][name]
}

// checkCatalogues checks the catalogues sc's lines fields name exist.
func (sc *Schema) checkCatalogues(set *SchemaSet) error {
	for _, f := range append(append([]*field{}, sc.fields...), sc.catalogueFields()...) {
		if f.kind != kindLines {
			continue
		}
		cat, ok := set.byName[RecordSchema(f.catalogue)]
		if !ok || cat.Step != StepCatalogue {
			return fmt.Errorf("field %s: %q is not a catalogue schema", f.name, f.catalogue)
		}
		if p := cat.catalogueField("unit_price"); p == nil {
			return fmt.Errorf("field %s: catalogue %q has no unit_price field", f.name, f.catalogue)
		}
		f.catalogueSchema = cat
	}
	return nil
}

func (sc *Schema) catalogueFields() []*field {
	if sc.catalogue == nil {
		return nil
	}
	return sc.catalogue.fields
}

func (sc *Schema) catalogueField(name string) *field {
	for _, f := range sc.catalogueFields() {
		if f.name == name {
			return f
		}
	}
	return nil
}

func profileNames() string {
	names := make([]string, len(Profiles))
	for i, p := range Profiles {
		names[i] = string(p)
	}
	return strings.Join(names, ", ")
}
