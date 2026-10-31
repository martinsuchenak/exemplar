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
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/martinsuchenak/exemplar/build"
)

// Record schemas are TOML files: the built-in ones in schemas/, embedded,
// plus any directory of files the server is given. A file combines the field
// types in schemaeval.go; it holds no logic of its own. See
// docs/generation.md for the format.

//go:embed schemas/*.toml
var builtinSchemaFiles embed.FS

// Sources say where a schema came from.
const (
	SourceBuiltin = "built-in"
	SourceFile    = "file"    // --record-schemas-dir
	SourceAPI     = "api"     // added through the API, kept in the database
	SourceRequest = "request" // sent with one generate request
)

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
	// Source is where it came from: built-in, file, api or request.
	Source string `json:"source"`
	// Version counts an API schema's changes, from 1.
	Version int `json:"version,omitempty"`
	// Problem says why an API schema is not loaded (see WithStoredSchemas);
	// such a schema is listed but cannot be generated.
	Problem string `json:"problem,omitempty"`
	// Hash identifies the file's content (8 hex digits of its SHA-256), so
	// fixture provenance names the exact definition that made a record.
	Hash string `json:"hash"`
	// Fields are the record's top-level keys besides "schema", in file order.
	Fields []string `json:"fields"`
	// Lists are the named lists the record's fields pick from; a request
	// may give its own for any of them.
	Lists []string `json:"lists,omitempty"`
	// RequestLists are the lists a request must give.
	RequestLists []string `json:"request_lists,omitempty"`
	// Exemplar is the oldest exemplar version the file works with.
	Exemplar string `json:"exemplar,omitempty"`

	file      string // where it was read from, for errors
	text      []byte // the file as given
	title     string
	titleToks []tmplTok
	links     []schemaLink
	fields    []*field
	catalogue *catalogue
	lists     map[string]map[string][]any // profile ("all" for every one) -> list
	// requestLists are the lists a request must give; recordLists are the
	// lists record fields (not catalogue entries) pick from.
	requestLists, recordLists map[string]bool
}

type schemaLink struct {
	role, value string
}

type catalogue struct {
	size   int
	fields []*field
}

// Text returns the schema file as it was given.
func (sc *Schema) Text() string {
	return string(sc.text)
}

// SchemaSet is the record schemas a server knows.
type SchemaSet struct {
	byName map[RecordSchema]*Schema
	// broken are API schemas that did not load, listed with their Problem
	// but never generated.
	broken []*Schema
}

// Current returns s itself, so a fixed set serves where a changing one
// (the server's, which the API changes) is expected.
func (s *SchemaSet) Current() *SchemaSet {
	return s
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

// List returns every schema, sorted by name, with the API schemas that did
// not load (their Problem set) among them.
func (s *SchemaSet) List() []*Schema {
	out := make([]*Schema, 0, len(s.byName)+len(s.broken))
	for _, n := range s.Names() {
		out = append(out, s.byName[n])
	}
	out = append(out, s.broken...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Problem returns why the API schema called name did not load, or "" when
// it loaded or is not an API schema.
func (s *SchemaSet) Problem(name RecordSchema) string {
	for _, sc := range s.broken {
		if sc.Name == name {
			return sc.Problem
		}
	}
	return ""
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
		sc.Builtin, sc.Source, sc.file = true, SourceBuiltin, "built-in "+e.Name()
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
			sc.Source, sc.file = SourceFile, f
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

// Limits on what one generate request sends.
const (
	// MaxRequestSchemas caps the schema files one request sends, and
	// MaxRequestSchemaBytes the size of each.
	MaxRequestSchemas     = 20
	MaxRequestSchemaBytes = 256 << 10
	// MaxRequestLists caps the lists one request gives, and
	// MaxRequestListItems the items in each.
	MaxRequestLists     = 100
	MaxRequestListItems = 10000
)

// WithRequestSchemas returns base plus the schema files a request sends,
// and their names in the order sent. They are checked as a server's files
// are and may use base's catalogues, but may not take a name base has: a
// request never changes what the server's own schemas give.
func WithRequestSchemas(base *SchemaSet, files []string) (*SchemaSet, []RecordSchema, error) {
	if len(files) > MaxRequestSchemas {
		return nil, nil, fmt.Errorf("at most %d schema files per request", MaxRequestSchemas)
	}
	set := base.clone()
	var added []*Schema
	for i, data := range files {
		if len(data) > MaxRequestSchemaBytes {
			return nil, nil, fmt.Errorf("schema file %d is over %d bytes", i+1, MaxRequestSchemaBytes)
		}
		sc, err := set.join(data, "", SourceRequest, fmt.Sprintf("schema file %d", i+1))
		if err != nil {
			return nil, nil, fmt.Errorf("schema file %d: %w", i+1, err)
		}
		added = append(added, sc)
	}
	failed := set.link(added)
	names := make([]RecordSchema, len(added))
	for i, sc := range added {
		if err := failed[sc.Name]; err != nil {
			return nil, nil, fmt.Errorf("%s (%s): %w", sc.file, sc.Name, err)
		}
		names[i] = sc.Name
	}
	return set, names, nil
}

// join parses a schema file that joins s (sent with a request, or added
// through the API) and adds it: it may not take a name s has, and must be
// called want unless want is empty. Its catalogues are linked with the rest
// of the newcomers (link).
func (s *SchemaSet) join(text string, want RecordSchema, source, file string) (*Schema, error) {
	sc, err := parseSchema("", []byte(text))
	if err != nil {
		return nil, err
	}
	if want != "" && sc.Name != want {
		return nil, fmt.Errorf("the file names %q, not %q", sc.Name, want)
	}
	if old, ok := s.byName[sc.Name]; ok {
		return nil, fmt.Errorf("%q is taken by %s schema; give it another name", sc.Name, sourceArticle[old.Source])
	}
	sc.Source, sc.file = source, file
	s.byName[sc.Name] = sc
	return sc, nil
}

// sourceArticle names a source in a sentence.
var sourceArticle = map[string]string{
	SourceBuiltin: "a built-in",
	SourceFile:    "a file",
	SourceAPI:     "an API",
	SourceRequest: "another request",
}

// link links the newcomers to their catalogues and returns those that
// fail, left out of s. One whose catalogue is left out fails in turn, so it
// checks again until nothing more fails. The schemas s already had are
// linked already and shared with sets in use, so they are never relinked.
func (s *SchemaSet) link(added []*Schema) map[RecordSchema]error {
	failed := map[RecordSchema]error{}
	for changed := true; changed; {
		changed = false
		for _, sc := range added {
			if failed[sc.Name] != nil {
				continue
			}
			if err := sc.checkCatalogues(s); err != nil {
				failed[sc.Name] = err
				delete(s.byName, sc.Name)
				changed = true
			}
		}
	}
	return failed
}

// clone copies s's map, so schemas can be added to the copy; the schemas
// themselves are shared.
func (s *SchemaSet) clone() *SchemaSet {
	set := &SchemaSet{byName: make(map[RecordSchema]*Schema, len(s.byName)), broken: s.broken}
	for n, sc := range s.byName {
		set.byName[n] = sc
	}
	return set
}

// Limits on the schemas added through the API.
const (
	MaxStoredSchemas     = 200
	MaxStoredSchemaBytes = MaxRequestSchemaBytes
)

// StoredSchema is a schema added through the API, as the database holds it.
type StoredSchema struct {
	Name    RecordSchema
	Version int
	Text    string
}

// WithStoredSchemas returns base plus the schemas added through the API.
// They are checked as a server's files are, may use base's catalogues and
// each other's, and may not take a name base has. One that fails, or uses
// the catalogue of one that fails, is left out and listed with its Problem
// instead of failing the rest: the server still starts, and an admin can
// fix it through the API. Each is parsed afresh, so the schemas of a set in
// use are never relinked.
func WithStoredSchemas(base *SchemaSet, stored []StoredSchema) *SchemaSet {
	set := base.clone()
	set.broken = nil
	failed := map[RecordSchema]error{}
	var added []*Schema
	for _, st := range stored {
		sc, err := set.join(st.Text, st.Name, SourceAPI, "API schema "+string(st.Name))
		if err != nil {
			failed[st.Name] = err
			continue
		}
		sc.Version = st.Version
		added = append(added, sc)
	}
	for n, err := range set.link(added) {
		failed[n] = err
	}
	for _, st := range stored {
		if err := failed[st.Name]; err != nil {
			// Its step and fields are unknown; the empty list keeps the
			// documented shape.
			set.broken = append(set.broken, &Schema{Name: st.Name, Source: SourceAPI, Version: st.Version, Fields: []string{}, Problem: err.Error(), text: []byte(st.Text)})
		}
	}
	return set
}

// ParseSchemaName checks a schema file on its own, as WithRequestSchemas
// does, and returns its name. Its catalogues are checked when it joins a
// set.
func ParseSchemaName(text string) (RecordSchema, error) {
	sc, err := parseSchema("", []byte(text))
	if err != nil {
		return "", err
	}
	return sc.Name, nil
}

// SchemaFileName reads only a schema file's name, for a client that needs
// it to address the schema; the server checks the rest.
func SchemaFileName(text string) (RecordSchema, error) {
	var def struct {
		Name string `toml:"name"`
	}
	if _, err := toml.Decode(text, &def); err != nil {
		return "", err
	}
	if !schemaNameRe.MatchString(def.Name) {
		return "", fmt.Errorf("name %q must be lower-case letters, digits or '-'", def.Name)
	}
	return RecordSchema(def.Name), nil
}

// CheckRequestLists checks the lists a request gives against the schemas it
// generates: each is a list one of them picks from (so a misspelt name is
// not quietly ignored), none is empty or too long, and every list they say
// a request must give is there.
func CheckRequestLists(schemas []*Schema, lists map[string][]any) error {
	if len(lists) > MaxRequestLists {
		return fmt.Errorf("at most %d lists per request", MaxRequestLists)
	}
	used := map[string]bool{}
	for _, sc := range schemas {
		for n := range sc.recordLists {
			used[n] = true
		}
		for n := range sc.requestLists {
			if _, ok := lists[n]; !ok {
				return fmt.Errorf("record schema %s needs list %q with the request", sc.Name, n)
			}
		}
	}
	for name, items := range lists {
		switch {
		case !used[name]:
			return fmt.Errorf("list %q: no schema in the request picks from it", name)
		case len(items) == 0:
			return fmt.Errorf("list %q is empty", name)
		case len(items) > MaxRequestListItems:
			return fmt.Errorf("list %q has over %d items", name, MaxRequestListItems)
		}
	}
	return nil
}

// schemaFile is a schema file as TOML decodes it.
type schemaFile struct {
	Name        string      `toml:"name"`
	Description string      `toml:"description"`
	Step        string      `toml:"step"`
	Title       string      `toml:"title"`
	Links       []linkDef   `toml:"links"`
	Fields      []fieldDef  `toml:"fields"`
	Catalogue   *catalogDef `toml:"catalogue"`
	// Lists are lists of values per profile, or under "all" for every
	// profile.
	Lists map[string]map[string][]any `toml:"lists"`
	// RequestLists names lists a request gives, such as a target
	// system's own status IDs.
	RequestLists []string `toml:"request_lists"`
	// Exemplar is the oldest version the file needs ("0.5").
	Exemplar string `toml:"exemplar"`
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
// must be the schema name plus ".toml"; empty for a file sent with a
// request, which has no name of its own.
func parseSchema(file string, data []byte) (*Schema, error) {
	// The version a file needs comes first, read on its own: a file for a
	// newer exemplar may hold keys or shapes this one cannot decode, and
	// the reason to give then is the version, not the key.
	var need struct {
		Exemplar string `toml:"exemplar"`
	}
	if _, err := toml.Decode(string(data), &need); err != nil {
		return nil, err
	}
	if err := checkExemplar(need.Exemplar, build.Version); err != nil {
		return nil, err
	}
	var def schemaFile
	md, err := toml.Decode(string(data), &def)
	if err != nil {
		return nil, err
	}
	for _, key := range md.Undecoded() {
		if !looseKey(key) {
			return nil, fmt.Errorf("unknown key %q", key.String())
		}
	}
	if !schemaNameRe.MatchString(def.Name) {
		return nil, fmt.Errorf("name %q must be lower-case letters, digits or '-'", def.Name)
	}
	if file != "" && file != def.Name+".toml" {
		return nil, fmt.Errorf("file must be named %s.toml after its schema", def.Name)
	}
	sum := sha256.Sum256(data)
	sc := &Schema{
		Name: RecordSchema(def.Name), Description: def.Description, Step: def.Step, Exemplar: def.Exemplar,
		Hash: hex.EncodeToString(sum[:4]), text: data, title: def.Title, lists: def.Lists,
		requestLists: map[string]bool{}, recordLists: map[string]bool{},
	}
	if sc.Step == "" {
		sc.Step = StepPerson
	}
	if sc.Step != StepPerson && sc.Step != StepCompany && sc.Step != StepCatalogue {
		return nil, fmt.Errorf("step must be person, company or catalogue, not %q", sc.Step)
	}
	if err := sc.checkLists(def.RequestLists); err != nil {
		return nil, err
	}
	top := &compileScope{sc: sc, sp: scopeRecord, known: map[string]*field{}, own: map[string]bool{}}
	if sc.Step == StepCatalogue {
		if def.Catalogue == nil || def.Catalogue.Size < 1 || def.Catalogue.Size > 10000 {
			return nil, fmt.Errorf("a catalogue schema needs [catalogue] with a size of 1-10000")
		}
		sc.catalogue = &catalogue{size: def.Catalogue.Size}
		entry := &compileScope{sc: sc, sp: scopeEntry, known: map[string]*field{}, own: map[string]bool{}}
		for i, fd := range def.Catalogue.Fields {
			f, err := compileField(fd, entry)
			if err != nil {
				return nil, fmt.Errorf("catalogue field %d (%s): %w", i+1, fd.Name, err)
			}
			entry.add(f)
			sc.catalogue.fields = append(sc.catalogue.fields, f)
		}
		if p := entry.known["unit_price"]; p != nil && p.kind != kindMoney {
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
			top.add(f)
		}
	} else if def.Catalogue != nil {
		return nil, fmt.Errorf("[catalogue] is only for step = \"catalogue\"")
	}
	for i, fd := range def.Fields {
		f, err := compileField(fd, top)
		if err != nil {
			return nil, fmt.Errorf("field %d (%s): %w", i+1, fd.Name, err)
		}
		top.add(f)
		sc.fields = append(sc.fields, f)
	}
	for _, f := range append(append([]*field{}, sc.catalogueFields()...), sc.fields...) {
		if !f.hidden {
			sc.Fields = append(sc.Fields, f.name)
		}
	}
	sc.Lists = sortedKeys(sc.recordLists)
	sc.RequestLists = sortedKeys(sc.requestLists)
	if strings.TrimSpace(sc.title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if sc.titleToks, err = compileTemplate(sc.title, top); err != nil {
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

// checkLists checks every list's profile is known, no list is empty and
// every item is a value JSON holds, and that the lists a request gives are
// not also in the file.
func (sc *Schema) checkLists(requestLists []string) error {
	for profile, lists := range sc.lists {
		if profile != "all" && !Profile(profile).Valid() {
			return fmt.Errorf("lists.%s: unknown profile (use all, %s)", profile, profileNames())
		}
		for name, items := range lists {
			if len(items) == 0 {
				return fmt.Errorf("lists.%s.%s is empty", profile, name)
			}
			for i, item := range items {
				v, err := jsonValue(item)
				if err != nil {
					return fmt.Errorf("lists.%s.%s: %w", profile, name, err)
				}
				items[i] = v
			}
		}
	}
	for _, name := range requestLists {
		if !listNameRe.MatchString(name) {
			return fmt.Errorf("request_lists: %q must be letters, digits, '_' or '-'", name)
		}
		for profile := range sc.lists {
			if _, ok := sc.lists[profile][name]; ok {
				return fmt.Errorf("request_lists: %q is also under lists.%s; a request may replace any list, so leave it out of request_lists", name, profile)
			}
		}
		sc.requestLists[name] = true
	}
	return nil
}

// listNameRe is what a list a request gives may be called.
var listNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// looseKey reports whether an undecoded TOML key is inside a value decoded
// loosely (a field's pick, text, sum, bool, const or optional, or a list),
// which the compiler checks itself.
func looseKey(key toml.Key) bool {
	if len(key) > 0 && key[0] == "lists" {
		return true
	}
	for i := 1; i < len(key); i++ {
		if key[i-1] == "fields" && anyKeys[key[i]] {
			return true
		}
	}
	return false
}

// exemplarVersionRe is a version a schema can require: major.minor, with an
// optional patch.
var exemplarVersionRe = regexp.MustCompile(`^(\d{1,6})\.(\d{1,6})(?:\.(\d{1,6}))?$`)

// releaseRe is a release build's version (v0.5.0 or 0.5.0). Anything else
// (dev, or git describe output for commits after a tag) is a build that may
// hold features newer than any release, so it takes every schema.
var releaseRe = regexp.MustCompile(`^v?(\d{1,6})\.(\d{1,6})\.(\d{1,6})$`)

// checkExemplar checks a schema's exemplar requirement, the oldest version
// it works with, against this build's version, the way go.mod's go line
// works: a newer exemplar reads an older schema, an older one refuses a
// newer schema with a reason rather than misreading it.
func checkExemplar(need, version string) error {
	if need == "" {
		return nil
	}
	m := exemplarVersionRe.FindStringSubmatch(need)
	if m == nil {
		return fmt.Errorf("exemplar = %q: give the oldest version it works with, such as \"0.5\"", need)
	}
	have := releaseRe.FindStringSubmatch(version)
	if have == nil {
		return nil
	}
	for i := 1; i <= 3; i++ {
		// The patterns allow only up to six digits, or nothing for a
		// patch the file leaves out, which counts as 0.
		n, _ := strconv.Atoi(m[i])
		h, _ := strconv.Atoi(have[i])
		if h != n {
			if h < n {
				return fmt.Errorf("needs exemplar %s or later; this is %s", need, version)
			}
			return nil
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// list returns the named list for profile.
func (sc *Schema) list(profile Profile, name string) []any {
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
