package synth

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
	"time"
)

// The field types a schema file combines. Each field draws from the
// record's generator in file order, so the same seed, people and file give
// the same record. Adding a field type here makes it available to every
// schema file.

type fieldKind int

const (
	kindValue fieldKind = iota + 1
	kindTemplate
	kindRef
	kindPick
	kindInt
	kindMoney
	kindDate
	kindText
	kindLines
	kindSum
	kindPercent
	kindNth
	kindBool
	kindConst
	kindObject
	kindList
)

const (
	// maxNesting caps how deeply objects and lists nest in one another.
	maxNesting = 4
	// maxListItems caps the items one list field makes.
	maxListItems = 100
	// maxLineItems caps the entries one lines field draws, for the same
	// reason as maxListItems.
	maxLineItems = 100
	// MaxRecordValues caps the values (fields, at any depth) one record
	// makes, so a schema of lists inside lists cannot make an enormous
	// record; generate caps a request's total size besides.
	MaxRecordValues = 100_000
	// chanceScale is the resolution of a bool's or optional's chance.
	chanceScale = 1_000_000
)

// fieldDef is one [[fields]] entry as TOML decodes it. Exactly one of the
// type keys is set.
type fieldDef struct {
	Name     string      `toml:"name"`
	Value    *string     `toml:"value"`
	Template *string     `toml:"template"`
	Ref      *string     `toml:"ref"`
	Pick     any         `toml:"pick"`
	Int      []int       `toml:"int"`
	Money    []int       `toml:"money"`
	Date     *dateDef    `toml:"date"`
	Text     any         `toml:"text"`
	Lines    *linesDef   `toml:"lines"`
	Sum      any         `toml:"sum"`
	Percent  *percentDef `toml:"percent"`
	Nth      *string     `toml:"nth"`
	Bool     any         `toml:"bool"`
	Const    any         `toml:"const"`
	Object   *bool       `toml:"object"`
	List     *listDef    `toml:"list"`
	Format   string      `toml:"format"`
	// Fields are an object's or a list item's own fields.
	Fields []fieldDef `toml:"fields"`
	// Optional is the chance (0-1) the field is null instead.
	Optional any `toml:"optional"`
	// Hidden fields are made and can be read by the fields below them,
	// but are left out of the record.
	Hidden bool `toml:"hidden"`
}

// anyKeys are the field keys decoded loosely, as any; parseSchema leaves
// what is inside them for compileField to check.
var anyKeys = set("pick", "text", "sum", "bool", "const", "optional")

type dateDef struct {
	WithinDays *int   `toml:"within_days"`
	After      string `toml:"after"`
	Days       []int  `toml:"days"`
	Months     []int  `toml:"months"`
}

type linesDef struct {
	Catalogue string `toml:"catalogue"`
	Count     []int  `toml:"count"`
	Qty       []int  `toml:"qty"`
}

type percentDef struct {
	Of   string  `toml:"of"`
	Rate float64 `toml:"rate"`
}

type listDef struct {
	Count []int  `toml:"count"`
	Each  string `toml:"each"`
}

// field is a compiled field.
type field struct {
	name     string
	kind     fieldKind
	format   string  // money: display, number or string; int: string; date: a Go time layout
	optional float64 // the chance it is null
	hidden   bool

	ref      string    // value: the name it reads
	template []tmplTok // template
	prefix   *string   // ref: nil for one of the default prefixes
	list     []any     // pick: a literal list
	listName string    // pick: a named list from [lists] or the request
	lo, hi   int       // int, money, date ranges, lines and list count
	qlo, qhi int       // lines quantity
	days     *int      // date: within days of the base date
	after    string    // date: the date field it follows
	months   bool      // date: the range is in months, not days
	text     string    // text: sentence, title, label, subject or paragraph
	sums     []string  // sum: money fields, or one "lines.column"
	of       string    // percent
	rate     float64   // percent
	chance   float64   // bool: the chance of true
	constVal any       // const
	children []*field  // object, list: the fields of the object or each item
	each     string    // list: "company.contacts" for an item per person

	catalogue       string  // lines: the catalogue schema's name
	catalogueSchema *Schema // lines: set once every file is loaded
}

type scope int

const (
	scopeRecord scope = iota
	scopeEntry        // a catalogue entry's fields
)

var (
	fieldNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	refPrefixRe = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)
	personAttrs = set("id", "first_name", "last_name", "name", "email", "phone", "job_title", "company", "address")
	companyAttr = set("name", "short", "domain", "abn", "phone", "website", "industry", "address", "contacts")
	addressAttr = set("street", "suburb", "state", "postcode", "country")
	textKinds   = set("sentence", "title", "label", "subject")
)

func set(values ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return m
}

// compileScope is what a field being compiled can see: the fields above it
// in its record, object or list item, then those above the object or list
// it is in, the nearest first (a nested field's name hides an outer one).
type compileScope struct {
	sc    *Schema
	sp    scope
	known map[string]*field
	// own are the names defined in this record, object or item, which
	// may not be defined twice.
	own    map[string]bool
	inItem bool // inside a list's item, where item.number can be read
	depth  int  // objects and lists this scope is inside
}

// nested returns the scope of an object's or list item's fields.
func (cs *compileScope) nested(inItem bool) *compileScope {
	known := make(map[string]*field, len(cs.known))
	for k, v := range cs.known {
		known[k] = v
	}
	return &compileScope{sc: cs.sc, sp: cs.sp, known: known, own: map[string]bool{}, inItem: cs.inItem || inItem, depth: cs.depth + 1}
}

// add makes f visible to the fields below it.
func (cs *compileScope) add(f *field) {
	cs.known[f.name] = f
	cs.own[f.name] = true
}

// compileField checks fd against the schema and the fields above it.
func compileField(fd fieldDef, cs *compileScope) (*field, error) {
	known := cs.known
	if !fieldNameRe.MatchString(fd.Name) {
		return nil, fmt.Errorf("name must be letters, digits or '_', starting with a letter")
	}
	if fd.Name == "schema" && cs.depth == 0 {
		return nil, fmt.Errorf("\"schema\" is set on every record and cannot be a field")
	}
	if cs.own[fd.Name] {
		return nil, fmt.Errorf("defined twice")
	}
	types := 0
	for _, given := range []bool{fd.Value != nil, fd.Template != nil, fd.Ref != nil, fd.Pick != nil, fd.Int != nil,
		fd.Money != nil, fd.Date != nil, fd.Text != nil, fd.Lines != nil, fd.Sum != nil, fd.Percent != nil, fd.Nth != nil,
		fd.Bool != nil, fd.Const != nil, fd.Object != nil, fd.List != nil} {
		if given {
			types++
		}
	}
	if types != 1 {
		return nil, fmt.Errorf("needs exactly one type key (value, template, ref, pick, int, money, date, text, lines, sum, percent, nth, bool, const, object or list)")
	}
	if len(fd.Fields) > 0 && fd.Object == nil && fd.List == nil {
		return nil, fmt.Errorf("fields are only for object and list")
	}
	f := &field{name: fd.Name, format: fd.Format, hidden: fd.Hidden}
	var err error
	if fd.Optional != nil {
		if f.optional, err = chance(fd.Optional, "optional"); err == nil && f.optional == 0 {
			err = fmt.Errorf("optional must be more than 0")
		}
		if err != nil {
			return nil, err
		}
	}
	if cs.sp == scopeEntry && f.optional > 0 {
		return nil, fmt.Errorf("catalogue entry fields cannot be optional")
	}
	switch {
	case fd.Value != nil:
		f.kind, f.ref = kindValue, *fd.Value
		err = checkName(f.ref, cs)
	case fd.Template != nil:
		f.kind = kindTemplate
		f.template, err = compileTemplate(*fd.Template, cs)
	case fd.Ref != nil:
		f.kind = kindRef
		if *fd.Ref != "" {
			if !refPrefixRe.MatchString(*fd.Ref) {
				return nil, fmt.Errorf("ref prefix must be 1-8 capital letters or digits")
			}
			f.prefix = fd.Ref
		}
	case fd.Pick != nil:
		f.kind = kindPick
		err = f.compilePick(fd.Pick, cs)
	case fd.Int != nil:
		f.kind = kindInt
		f.lo, f.hi, err = span(fd.Int, "int")
	case fd.Money != nil:
		f.kind = kindMoney
		f.lo, f.hi, err = span(fd.Money, "money")
		if err == nil && f.lo < 0 {
			err = fmt.Errorf("money must not be negative")
		}
	case fd.Date != nil:
		f.kind = kindDate
		err = f.compileDate(*fd.Date, known)
	case fd.Text != nil:
		f.kind = kindText
		err = f.compileText(fd.Text)
	case fd.Lines != nil:
		if cs.sp == scopeEntry {
			return nil, fmt.Errorf("a catalogue entry cannot have lines")
		}
		f.kind, f.catalogue = kindLines, fd.Lines.Catalogue
		if f.catalogue == "" {
			return nil, fmt.Errorf("lines needs a catalogue")
		}
		if f.lo, f.hi, err = span(fd.Lines.Count, "lines count"); err == nil {
			f.qlo, f.qhi, err = span(fd.Lines.Qty, "lines qty")
		}
		if err == nil && f.lo < 1 {
			err = fmt.Errorf("lines count must be at least 1")
		}
		if err == nil && f.hi > maxLineItems {
			err = fmt.Errorf("lines count must be within 1-%d", maxLineItems)
		}
	case fd.Sum != nil:
		f.kind = kindSum
		err = f.compileSum(fd.Sum, known)
	case fd.Nth != nil:
		// A catalogue entry's item of a list, in order, so entries do not
		// repeat one; parseSchema checks the catalogue is no longer.
		if cs.sp != scopeEntry {
			return nil, fmt.Errorf("nth is only for catalogue entries")
		}
		f.kind = kindNth
		err = f.compilePick(*fd.Nth, cs)
		if err == nil && f.listName == "" {
			err = fmt.Errorf("nth takes \"@name\", a list under [lists]")
		}
	case fd.Percent != nil:
		f.kind, f.of, f.rate = kindPercent, fd.Percent.Of, fd.Percent.Rate
		if g := known[f.of]; g == nil || !g.isMoney() {
			err = fmt.Errorf("percent of %q: not a money field above this one", f.of)
		} else if g.optional > 0 {
			err = fmt.Errorf("percent of %q: an optional field can be null", f.of)
		} else if f.rate < 0 || f.rate > 1000 {
			err = fmt.Errorf("percent rate must be 0-1000")
		}
	case fd.Bool != nil:
		f.kind = kindBool
		f.chance, err = chance(fd.Bool, "bool")
	case fd.Const != nil:
		f.kind = kindConst
		f.constVal, err = jsonValue(fd.Const)
	case fd.Object != nil:
		if !*fd.Object {
			return nil, fmt.Errorf("object takes true, with the object's [[fields]] under it")
		}
		f.kind = kindObject
		err = f.compileChildren(fd.Fields, cs, false)
	case fd.List != nil:
		f.kind = kindList
		if err = f.compileList(*fd.List, cs); err == nil {
			err = f.compileChildren(fd.Fields, cs, true)
		}
	}
	if err != nil {
		return nil, err
	}
	return f, f.checkFormat()
}

// chance reads a probability from 0 to 1.
func chance(v any, what string) (float64, error) {
	var p float64
	switch n := v.(type) {
	case int64:
		p = float64(n)
	case float64:
		p = n
	default:
		return 0, fmt.Errorf("%s takes a chance from 0 to 1", what)
	}
	if p < 0 || p > 1 {
		return 0, fmt.Errorf("%s takes a chance from 0 to 1", what)
	}
	return p, nil
}

// compileList checks a list's count or each.
func (f *field) compileList(l listDef, cs *compileScope) error {
	switch {
	case cs.sp == scopeEntry || cs.sc.Step == StepCatalogue:
		return fmt.Errorf("catalogue records cannot have lists")
	case l.Each != "" && l.Count != nil:
		return fmt.Errorf("list takes count or each, not both")
	case l.Each != "":
		if l.Each != "company.contacts" {
			return fmt.Errorf("list each must be company.contacts")
		}
		f.each = l.Each
		return nil
	}
	var err error
	if f.lo, f.hi, err = span(l.Count, "list count"); err == nil && (f.lo < 0 || f.hi > maxListItems) {
		err = fmt.Errorf("list count must be within 0-%d", maxListItems)
	}
	return err
}

// compileChildren compiles an object's or list item's own fields, which see
// the fields above the object or list too.
func (f *field) compileChildren(defs []fieldDef, cs *compileScope, item bool) error {
	if cs.sp == scopeEntry {
		return fmt.Errorf("a catalogue entry cannot have objects or lists")
	}
	if cs.depth+1 > maxNesting {
		return fmt.Errorf("objects and lists nest at most %d deep", maxNesting)
	}
	if len(defs) == 0 {
		return fmt.Errorf("needs its own [[fields]] under it")
	}
	inner := cs.nested(item)
	for i, fd := range defs {
		c, err := compileField(fd, inner)
		if err != nil {
			return fmt.Errorf("field %d (%s): %w", i+1, fd.Name, err)
		}
		inner.add(c)
		f.children = append(f.children, c)
	}
	return nil
}

// child returns the object's or item's own field called name.
func (f *field) child(name string) *field {
	for _, c := range f.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// isMoney reports whether the field holds an amount.
func (f *field) isMoney() bool {
	return f.kind == kindMoney || f.kind == kindSum || f.kind == kindPercent
}

func (f *field) checkFormat() error {
	switch {
	case f.isMoney():
		if f.format == "" {
			f.format = "display"
		}
		if f.format != "display" && f.format != "number" && f.format != "string" {
			return fmt.Errorf("format must be display, number or string")
		}
	case f.kind == kindInt:
		if f.format != "" && f.format != "string" {
			return fmt.Errorf("an int's format can only be string")
		}
	case f.kind == kindDate:
		if f.format == "" {
			f.format = "2006-01-02"
		}
	case f.format != "":
		return fmt.Errorf("format is only for money, int and date fields")
	}
	return nil
}

func (f *field) compilePick(v any, cs *compileScope) error {
	sc := cs.sc
	switch p := v.(type) {
	case string:
		name, ok := strings.CutPrefix(p, "@")
		if !ok {
			return fmt.Errorf("pick takes a list, or \"@name\" for a list under [lists]")
		}
		if sc.requestLists[name] {
			// The request gives it, so it cannot reach a catalogue entry,
			// which depends on the schema file alone.
			if cs.sp == scopeEntry {
				return fmt.Errorf("list %q comes with the request, and catalogue entries cannot use one", name)
			}
		} else {
			for _, profile := range Profiles {
				if len(sc.list(profile, name)) == 0 {
					return fmt.Errorf("list %q is missing for profile %s (put it under lists.all, or name it in request_lists)", name, profile)
				}
			}
		}
		f.listName = name
		if cs.sp == scopeRecord {
			sc.recordLists[name] = true
		}
	case []any:
		for _, item := range p {
			v, err := jsonValue(item)
			if err != nil {
				return fmt.Errorf("pick: %w", err)
			}
			f.list = append(f.list, v)
		}
		if len(f.list) == 0 {
			return fmt.Errorf("pick list is empty")
		}
	default:
		return fmt.Errorf("pick takes a list or \"@name\"")
	}
	return nil
}

// jsonValue converts a TOML value to one JSON holds as it is: a string,
// number, true or false, list or table of them. TOML dates and times are
// refused: a date field makes dates, and a const date is a string.
func jsonValue(v any) (any, error) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("nan and inf are not numbers JSON can hold")
		}
		return x, nil
	case nil, string, bool, int64:
		return x, nil
	case int:
		return int64(x), nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			var err error
			if out[i], err = jsonValue(item); err != nil {
				return nil, err
			}
		}
		return out, nil
	case []map[string]any:
		out := make([]any, len(x))
		for i, item := range x {
			var err error
			if out[i], err = jsonValue(item); err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			var err error
			if out[k], err = jsonValue(item); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("values are strings, numbers, true or false, lists or tables (write a date as a string)")
}

func (f *field) compileDate(d dateDef, known map[string]*field) error {
	switch {
	case d.WithinDays != nil && d.After == "" && d.Days == nil && d.Months == nil:
		if *d.WithinDays < 0 {
			return fmt.Errorf("within_days must not be negative")
		}
		f.days = d.WithinDays
		return nil
	case d.WithinDays == nil && d.After != "":
		if g := known[d.After]; g == nil || g.kind != kindDate {
			return fmt.Errorf("after %q: not a date field above this one", d.After)
		} else if g.optional > 0 {
			return fmt.Errorf("after %q: an optional field can be null", d.After)
		}
		f.after = d.After
		r := d.Days
		if d.Months != nil {
			if d.Days != nil {
				return fmt.Errorf("give days or months, not both")
			}
			r, f.months = d.Months, true
		}
		var err error
		f.lo, f.hi, err = span(r, "date range")
		return err
	}
	return fmt.Errorf("date takes { within_days = N } or { after = \"field\", days|months = [low, high] }")
}

func (f *field) compileText(v any) error {
	switch t := v.(type) {
	case string:
		if !textKinds[t] {
			return fmt.Errorf("text must be sentence, title, label, subject or { paragraph = [min, extra] }")
		}
		f.text = t
		return nil
	case map[string]any:
		p, ok := t["paragraph"].([]any)
		if len(t) != 1 || !ok || len(p) != 2 {
			return fmt.Errorf("text paragraph takes [min, extra] sentences")
		}
		lo, ok1 := p[0].(int64)
		extra, ok2 := p[1].(int64)
		if !ok1 || !ok2 || lo < 1 || extra < 0 {
			return fmt.Errorf("text paragraph takes [min, extra] with min at least 1")
		}
		f.text, f.lo, f.hi = "paragraph", int(lo), int(extra)
		return nil
	}
	return fmt.Errorf("text must be sentence, title, label, subject or { paragraph = [min, extra] }")
}

func (f *field) compileSum(v any, known map[string]*field) error {
	var names []string
	switch s := v.(type) {
	case string:
		names = []string{s}
	case []any:
		for _, item := range s {
			n, ok := item.(string)
			if !ok {
				return fmt.Errorf("sum takes field names")
			}
			names = append(names, n)
		}
	default:
		return fmt.Errorf("sum takes a field name or a list of them")
	}
	if len(names) == 0 {
		return fmt.Errorf("sum needs a field")
	}
	for _, n := range names {
		if rows, column, ok := strings.Cut(n, "."); ok {
			g := known[rows]
			if g == nil || (g.kind != kindLines && g.kind != kindList) || len(names) != 1 || column == "" || column == "qty" {
				return fmt.Errorf("sum %q: a lines or list field's money column, on its own", n)
			}
			if g.optional > 0 {
				return fmt.Errorf("sum %q: an optional field can be null", n)
			}
			// A lines column is checked once the catalogue is known; see
			// checkCatalogues. A list's is one of its items' fields.
			if g.kind == kindList {
				if c := g.child(column); c == nil || !c.isMoney() || c.optional > 0 {
					return fmt.Errorf("sum %q: %s's items have no money field %q that is never null", n, rows, column)
				}
			}
			continue
		}
		if g := known[n]; g == nil || !g.isMoney() {
			return fmt.Errorf("sum %q: not a money field above this one", n)
		} else if g.optional > 0 {
			return fmt.Errorf("sum %q: an optional field can be null", n)
		}
	}
	f.sums = names
	return nil
}

func span(r []int, what string) (int, int, error) {
	if len(r) != 2 || r[0] > r[1] {
		return 0, 0, fmt.Errorf("%s takes [low, high] with low <= high", what)
	}
	return r[0], r[1], nil
}

// checkName checks a name a value or template reads: an attribute of the
// record's person or company, the record's or item's number, a field above
// this one, or a key inside one (status.id for a picked { id, name }).
func checkName(name string, cs *compileScope) error {
	if cs.known[name] != nil {
		return nil
	}
	head, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		return fmt.Errorf("%q is not a field above this one", name)
	}
	switch head {
	case "record":
		if cs.sp == scopeEntry {
			return fmt.Errorf("%q: a catalogue entry has no record number", name)
		}
		if rest != "number" {
			return fmt.Errorf("%q: unknown attribute (record.number)", name)
		}
		return nil
	case "item":
		if !cs.inItem {
			return fmt.Errorf("%q: item.number is only for a list's items", name)
		}
		if rest != "number" {
			return fmt.Errorf("%q: unknown attribute (item.number)", name)
		}
		return nil
	case "person", "company":
		if cs.sp == scopeEntry || cs.sc.Step == StepCatalogue {
			return fmt.Errorf("%q: catalogue records are about no person or company", name)
		}
		attr, sub, _ := strings.Cut(rest, ".")
		attrs := personAttrs
		if head == "company" {
			attrs = companyAttr
		}
		if !attrs[attr] || (sub != "" && (attr != "address" || !addressAttr[sub])) {
			return fmt.Errorf("%q: unknown attribute", name)
		}
		return nil
	}
	if f := cs.known[head]; f != nil {
		return f.checkPath(strings.Split(rest, "."), name)
	}
	return fmt.Errorf("%q: names are person.*, company.*, record.number, item.number, a field above this one or a key in one", name)
}

// checkPath checks a key path into f's value where it can be known: an
// object's fields, a const, a literal pick list. A list given by name may
// come with the request, so a missing key there fails when generating.
func (f *field) checkPath(path []string, name string) error {
	switch f.kind {
	case kindObject:
		c := f.child(path[0])
		if c == nil || c.hidden {
			return fmt.Errorf("%q: object %s has no field %q", name, f.name, path[0])
		}
		if len(path) == 1 {
			return nil
		}
		return c.checkPath(path[1:], name)
	case kindConst:
		if _, ok := pathIn(f.constVal, path); !ok {
			return fmt.Errorf("%q: const %s has no key %s", name, f.name, strings.Join(path, "."))
		}
		return nil
	case kindPick, kindNth:
		for _, item := range f.list {
			if _, ok := pathIn(item, path); !ok {
				return fmt.Errorf("%q: an item of pick %s has no key %s", name, f.name, strings.Join(path, "."))
			}
		}
		return nil
	case kindValue:
		return nil // reads a field or key the value's own check covered
	}
	return fmt.Errorf("%q: %s holds no keys to read", name, f.name)
}

// pathIn returns the value at path inside v's tables.
func pathIn(v any, path []string) (any, bool) {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// tmplTok is literal text, or a placeholder: alternatives tried in order,
// the first that is not empty filling it ({company.short|person.name}).
type tmplTok struct {
	literal string
	alts    []tmplAlt
}

// tmplAlt is a name to read, or a text generator to call.
type tmplAlt struct {
	name string
	text string
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z0-9_.|]+)\}`)

// compileTemplate parses "{label} - {company.short|person.name}": names to
// read, and text generators ({label}, {title}, {subject}, {sentence}) that
// draw like a text field. Alternatives are tried left to right, and only as
// far as the first that is not empty, so a generator after a name draws only
// when the name is empty. A title is filled after the fields, so its draws
// come last.
func compileTemplate(s string, cs *compileScope) ([]tmplTok, error) {
	var toks []tmplTok
	last := 0
	for _, m := range placeholderRe.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			toks = append(toks, tmplTok{literal: s[last:m[0]]})
		}
		var tok tmplTok
		for _, name := range strings.Split(s[m[2]:m[3]], "|") {
			switch {
			case name == "":
				return nil, fmt.Errorf("template %q has an empty alternative", s)
			case textKinds[name] && cs.known[name] == nil:
				tok.alts = append(tok.alts, tmplAlt{text: name})
			default:
				if err := checkName(name, cs); err != nil {
					return nil, err
				}
				tok.alts = append(tok.alts, tmplAlt{name: name})
			}
		}
		toks = append(toks, tok)
		last = m[1]
	}
	if last < len(s) {
		toks = append(toks, tmplTok{literal: s[last:]})
	}
	for _, t := range toks {
		if strings.ContainsAny(t.literal, "{}") {
			return nil, fmt.Errorf("template %q has a malformed placeholder", s)
		}
	}
	return toks, nil
}

// val is one evaluated field.
type val struct {
	json   any       // the value as it goes into the record
	text   string    // the value in a template
	cents  int64     // money
	when   time.Time // date
	rows   []map[string]val
	isDate bool
}

// evalContext is what one record, object or list item is about.
type evalContext struct {
	g       *Gen
	profile Profile
	schema  *Schema
	person  Person
	company Company
	people  []Person // the request's people, for company.contacts
	entryN  int      // the catalogue entry being made
	recordN int      // the record's number in its schema, from 1
	itemN   int      // the list item's number, from 1
	lists   map[string][]any
	vals    map[string]val
	// values counts down the values the record may still make; it is
	// shared by every context of one record.
	values *int
}

// Record generates the index-th record of schema s, counting records of that
// schema only. Each person-stepped schema starts at a different person,
// spaced evenly through People by its Slot among Slots (the request's
// schemas), and steps on one person per record, so a schema repeats a person
// only after it has used every one. Company records step through the
// distinct companies; catalogue records through the catalogue's entries.
func (g *Gen) Record(s *Schema, in RecordInput, index int) (Record, error) {
	if s == nil {
		return Record{}, fmt.Errorf("no record schema")
	}
	if !in.Profile.Valid() {
		in.Profile = ProfileInsurance
	}
	values := MaxRecordValues
	c := &evalContext{g: g, profile: in.Profile, schema: s, people: in.People, recordN: index + 1, lists: in.Lists, vals: map[string]val{}, values: &values}
	out := map[string]any{"schema": s.Name}
	switch s.Step {
	case StepCatalogue:
		entry, err := s.entry(in.Profile, index%s.catalogue.size, c.values)
		if err != nil {
			return Record{}, err
		}
		for name, v := range entry {
			c.vals[name] = v
			if !s.catalogueField(name).hidden {
				out[name] = v.json
			}
		}
	default:
		if len(in.People) == 0 {
			return Record{}, fmt.Errorf("a record needs at least one person")
		}
		slots := max(in.Slots, 1)
		c.person = in.People[personIndex(in.Slot, slots, index, len(in.People))]
		if s.Step == StepCompany {
			var ok bool
			if c.person, ok = companyPerson(in.People, index); !ok {
				return Record{}, fmt.Errorf("%s records need people who work for a company", s.Name)
			}
		}
		// A person without a company is an individual: every company value
		// reads as empty, rather than a company made up from no name.
		if c.person.Company != "" {
			c.company = CompanyFor(c.person.Company)
		}
	}
	if err := c.fields(s.fields, out); err != nil {
		return Record{}, fmt.Errorf("record schema %s, %w", s.Name, err)
	}
	title, err := c.template(s.titleToks)
	if err != nil {
		return Record{}, fmt.Errorf("record schema %s, title: %w", s.Name, err)
	}
	rec := Record{Schema: s.Name, Title: title, Data: mustJSON(out)}
	for _, l := range s.links {
		for _, id := range c.linked(l.value) {
			rec.People = append(rec.People, PersonRole{id, l.role})
		}
	}
	return rec, nil
}

// fields evaluates fs in order into c.vals and out, leaving hidden fields
// out of out.
func (c *evalContext) fields(fs []*field, out map[string]any) error {
	for _, f := range fs {
		v, err := c.eval(f)
		if err != nil {
			return fmt.Errorf("field %s: %w", f.name, err)
		}
		c.vals[f.name] = v
		if !f.hidden {
			out[f.name] = v.json
		}
	}
	return nil
}

// nested returns the context of an object or list item about p: it reads
// the fields above it as they are, and its own fields hide outer ones.
func (c *evalContext) nested(p Person, itemN int) *evalContext {
	n := *c
	n.person, n.itemN = p, itemN
	if p.Company != c.person.Company {
		n.company = Company{}
		if p.Company != "" {
			n.company = CompanyFor(p.Company)
		}
	}
	n.vals = make(map[string]val, len(c.vals))
	for k, v := range c.vals {
		n.vals[k] = v
	}
	return &n
}

// personIndex is the person the index-th record of the schema in slot is
// about: slot s of n starts at s*ceil(people/n), so with 20 people and five
// schemas they start at 0, 4, 8, 12 and 16.
func personIndex(slot, slots, index, n int) int {
	step := (n + slots - 1) / slots
	return (index + slot*step) % n
}

// entry is catalogue entry n for profile. It depends on the schema, the
// profile and n only, never on a request's seed, so entry n is the same in
// every request, dataset and instance, and a quote's lines always match the
// catalogue's records.
func (s *Schema) entry(profile Profile, n int, budget *int) (map[string]val, error) {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "catalogue\x00%s\x00%s\x00%d", s.Name, profile, n)
	// The entry draws from the caller's budget, so a record full of lines
	// cannot make every entry's work invisible to the record's cap; nil
	// (a caller outside a record) gives the entry its own.
	if budget == nil {
		own := MaxRecordValues
		budget = &own
	}
	c := &evalContext{g: New(h.Sum64()), profile: profile, schema: s, entryN: n, vals: map[string]val{}, values: budget}
	for _, f := range s.catalogue.fields {
		v, err := c.eval(f)
		if err != nil {
			return nil, fmt.Errorf("catalogue %s entry %d, field %s: %w", s.Name, n, f.name, err)
		}
		c.vals[f.name] = v
	}
	return c.vals, nil
}

func (c *evalContext) eval(f *field) (val, error) {
	g := c.g
	if *c.values <= 0 {
		return val{}, fmt.Errorf("a record makes at most %d values", MaxRecordValues)
	}
	*c.values--
	// An optional field draws once more, before its value; a null draws
	// nothing else.
	if f.optional > 0 && g.Intn(chanceScale) < int(f.optional*chanceScale) {
		return val{json: nil}, nil
	}
	switch f.kind {
	case kindValue:
		return c.read(f.ref)
	case kindTemplate:
		s, err := c.template(f.template)
		return val{json: s, text: s}, err
	case kindRef:
		s := ""
		if f.prefix == nil {
			s = g.Reference()
		} else {
			s = fmt.Sprintf("%s-%06d", *f.prefix, g.Intn(1000000))
		}
		return val{json: s, text: s}, nil
	case kindPick:
		list := f.list
		if f.listName != "" {
			list = c.list(f.listName)
			if len(list) == 0 {
				return val{}, fmt.Errorf("list %q is not given (it comes with the request)", f.listName)
			}
		}
		return anyVal(list[g.Intn(len(list))]), nil
	case kindInt:
		n := g.Between(f.lo, f.hi)
		v := val{json: n, text: fmt.Sprint(n)}
		if f.format == "string" {
			v.json = v.text
		}
		return v, nil
	case kindMoney:
		return moneyVal(int64(g.Between(f.lo*100, f.hi*100)), f.format), nil
	case kindDate:
		var t time.Time
		switch {
		case f.days != nil:
			t = g.Date(*f.days)
		case f.months:
			t = c.vals[f.after].when.AddDate(0, g.Between(f.lo, f.hi), 0)
		default:
			t = c.vals[f.after].when.AddDate(0, 0, g.Between(f.lo, f.hi))
		}
		s := t.Format(f.format)
		return val{json: s, text: s, when: t, isDate: true}, nil
	case kindText:
		s := c.text(f.text, f.lo, f.hi)
		return val{json: s, text: s}, nil
	case kindLines:
		return c.lines(f)
	case kindSum:
		var cents int64
		if rows, column, ok := strings.Cut(f.sums[0], "."); ok {
			for _, row := range c.vals[rows].rows {
				cents += row[column].cents
			}
		} else {
			for _, n := range f.sums {
				cents += c.vals[n].cents
			}
		}
		return moneyVal(cents, f.format), nil
	case kindPercent:
		return moneyVal(int64(math.Round(float64(c.vals[f.of].cents)*f.rate/100)), f.format), nil
	case kindNth:
		list := c.schema.list(c.profile, f.listName)
		return anyVal(list[c.entryN%len(list)]), nil
	case kindBool:
		b := g.Intn(chanceScale) < int(f.chance*chanceScale)
		return val{json: b, text: fmt.Sprint(b)}, nil
	case kindConst:
		return anyVal(f.constVal), nil
	case kindObject:
		out := map[string]any{}
		if err := c.nested(c.person, c.itemN).fields(f.children, out); err != nil {
			return val{}, err
		}
		// Its text, the JSON, is made only if a template reads it.
		return val{json: out}, nil
	case kindList:
		return c.listItems(f)
	}
	return val{}, fmt.Errorf("unknown field type")
}

// listItems makes a list field's items: one per person at the company, or a
// count of them about the record's person. Each item's own values are kept
// as a row, so a sum can add up a money column.
func (c *evalContext) listItems(f *field) (val, error) {
	var about []Person
	if f.each != "" {
		about = c.contacts()
	} else {
		for range c.g.Between(f.lo, f.hi) {
			about = append(about, c.person)
		}
	}
	out := make([]any, 0, len(about))
	var rows []map[string]val
	for i, p := range about {
		n := c.nested(p, i+1)
		item := map[string]any{}
		if err := n.fields(f.children, item); err != nil {
			return val{}, fmt.Errorf("item %d: %w", i+1, err)
		}
		row := map[string]val{}
		for _, ch := range f.children {
			row[ch.name] = n.vals[ch.name]
		}
		out, rows = append(out, item), append(rows, row)
	}
	return val{json: out, rows: rows}, nil
}

// list returns a named list: the request's, else the schema's for the
// profile.
func (c *evalContext) list(name string) []any {
	if l, ok := c.lists[name]; ok {
		return l
	}
	return c.schema.list(c.profile, name)
}

// textOf is v's text in a template: an object's or list's JSON, made only
// when a template reads one.
func (v val) textOf() string {
	switch v.json.(type) {
	case map[string]any, []any:
		if v.text == "" {
			return anyVal(v.json).text
		}
	}
	return v.text
}

// anyVal is a value from a list or const, or one read inside a field: its
// text is itself for a string and its JSON otherwise.
func anyVal(v any) val {
	switch x := v.(type) {
	case nil:
		return val{json: nil}
	case string:
		return val{json: x, text: x}
	}
	b, err := json.Marshal(v)
	if err != nil {
		// Every value here came from TOML or JSON, or from fields.
		panic(fmt.Sprintf("encoding a value: %v", err))
	}
	return val{json: v, text: string(b)}
}

// lines picks distinct catalogue entries, each with a quantity and the
// amount it comes to. Every entry's fields draw from the record's value
// budget, so a lines field cannot make work the record's cap cannot see.
func (c *evalContext) lines(f *field) (val, error) {
	cat := f.catalogueSchema
	n := min(c.g.Between(f.lo, f.hi), cat.catalogue.size)
	used := map[int]bool{}
	price := cat.catalogueField("unit_price")
	var rows []map[string]val
	var out []map[string]any
	for len(rows) < n {
		if len(cat.catalogue.fields) > *c.values {
			return val{}, fmt.Errorf("lines needs %d values for another entry, of the %d the record may still make; ask for fewer lines", len(cat.catalogue.fields), *c.values)
		}
		i := c.g.Intn(cat.catalogue.size)
		if used[i] {
			continue
		}
		used[i] = true
		row, err := cat.entry(c.profile, i, c.values)
		if err != nil {
			return val{}, err
		}
		qty := c.g.Between(f.qlo, f.qhi)
		row["qty"] = val{json: qty, text: fmt.Sprint(qty)}
		row["amount"] = moneyVal(row["unit_price"].cents*int64(qty), price.format)
		js := map[string]any{}
		for k, v := range row {
			if e := cat.catalogueField(k); e == nil || !e.hidden { // qty and amount are no entry fields
				js[k] = v.json
			}
		}
		rows, out = append(rows, row), append(out, js)
	}
	return val{json: out, rows: rows}, nil
}

func (c *evalContext) text(kind string, lo, extra int) string {
	switch kind {
	case "title":
		return c.g.Title(c.profile)
	case "label":
		return c.g.Label(c.profile)
	case "subject":
		return c.g.Subject(c.profile)
	case "paragraph":
		return c.g.Paragraph(c.profile, lo, extra)
	}
	return c.g.Sentence(c.profile)
}

func (c *evalContext) template(toks []tmplTok) (string, error) {
	var b strings.Builder
	for _, t := range toks {
		if t.alts == nil {
			b.WriteString(t.literal)
			continue
		}
		for _, a := range t.alts {
			s := ""
			if a.text != "" {
				s = c.text(a.text, 0, 0)
			} else {
				v, err := c.read(a.name)
				if err != nil {
					return "", err
				}
				s = v.textOf()
			}
			if s != "" {
				b.WriteString(s)
				break
			}
		}
	}
	return b.String(), nil
}

// read returns a person or company attribute, the record's or item's
// number, an earlier field, or a key inside one.
func (c *evalContext) read(name string) (val, error) {
	if v, ok := c.vals[name]; ok {
		return v, nil
	}
	head, rest, _ := strings.Cut(name, ".")
	attr, sub, _ := strings.Cut(rest, ".")
	p, co := c.person, c.company
	var v any
	switch head + "." + attr {
	case "record.number":
		return val{json: c.recordN, text: fmt.Sprint(c.recordN)}, nil
	case "item.number":
		return val{json: c.itemN, text: fmt.Sprint(c.itemN)}, nil
	case "person.id":
		v = p.ID
	case "person.first_name":
		v = p.FirstName
	case "person.last_name":
		v = p.LastName
	case "person.name":
		v = p.Name()
	case "person.email":
		v = p.Email
	case "person.phone":
		v = p.Phone
	case "person.job_title":
		v = p.JobTitle
	case "person.company":
		v = p.Company
	case "person.address":
		v = addressVal(p.Address, sub)
	case "company.name":
		v = co.Name
	case "company.short":
		v = trimSuffix(co.Name)
	case "company.domain":
		v = co.Domain
	case "company.abn":
		v = co.ABN
	case "company.phone":
		v = co.Phone
	case "company.website":
		v = co.Website
	case "company.industry":
		v = co.Industry
	case "company.address":
		if co.Name == "" && sub == "" {
			return val{json: nil}, nil
		}
		v = addressVal(co.Address, sub)
	case "company.contacts":
		ids := c.linked("company.contacts")
		if ids == nil {
			ids = []string{}
		}
		return val{json: ids, text: strings.Join(ids, ", ")}, nil
	default:
		// A key inside a field above: checked at load where it can be,
		// else (a list the request gives) here.
		f, ok := c.vals[head]
		if !ok {
			return val{}, fmt.Errorf("%q is not a field above this one", name)
		}
		if f.json == nil {
			return val{json: nil}, nil
		}
		x, ok := pathIn(f.json, strings.Split(rest, "."))
		if !ok {
			return val{}, fmt.Errorf("%q: %s has no key %s", name, head, rest)
		}
		return anyVal(x), nil
	}
	if a, ok := v.(Address); ok {
		return val{json: a, text: a.Street + ", " + a.Suburb + " " + a.State + " " + a.Postcode}, nil
	}
	s, _ := v.(string)
	return val{json: v, text: s}, nil
}

func addressVal(a Address, sub string) any {
	switch sub {
	case "street":
		return a.Street
	case "suburb":
		return a.Suburb
	case "state":
		return a.State
	case "postcode":
		return a.Postcode
	case "country":
		return a.Country
	}
	return a
}

// linked returns the person IDs a link value names. A person without a
// company has no company contacts.
func (c *evalContext) linked(value string) []string {
	if value == "person.id" {
		return []string{c.person.ID}
	}
	var ids []string
	for _, q := range c.contacts() {
		ids = append(ids, q.ID)
	}
	return ids
}

// contacts returns the request's people at the person's company, none for
// a person without one.
func (c *evalContext) contacts() []Person {
	if c.person.Company == "" {
		return nil
	}
	var out []Person
	for _, q := range c.people {
		if q.Company == c.person.Company {
			out = append(out, q)
		}
	}
	return out
}

// moneyVal shows cents as "$1,234.56" (display), 1234.56 (number) or
// "1234.56" (string).
func moneyVal(cents int64, format string) val {
	display := formatMoney(int(cents))
	v := val{cents: cents, text: display, json: display}
	plain := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	switch format {
	case "number":
		v.json = json.Number(plain)
	case "string":
		v.json = plain
	}
	return v
}
