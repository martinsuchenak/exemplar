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
	Format   string      `toml:"format"`
}

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

// field is a compiled field.
type field struct {
	name   string
	kind   fieldKind
	format string // money: display or number; date: a Go time layout

	ref      string    // value: the name it reads
	template []tmplTok // template
	prefix   *string   // ref: nil for one of the default prefixes
	list     []string  // pick: a literal list
	listName string    // pick: a named list from [lists]
	lo, hi   int       // int, money, date ranges, lines count
	qlo, qhi int       // lines quantity
	days     *int      // date: within days of the base date
	after    string    // date: the date field it follows
	months   bool      // date: the range is in months, not days
	text     string    // text: sentence, title, label, subject or paragraph
	sums     []string  // sum: money fields, or one "lines.column"
	of       string    // percent
	rate     float64   // percent

	catalogue       string  // lines: the catalogue schema's name
	catalogueSchema *Schema // lines: set once every file is loaded
}

type scope int

const (
	scopeRecord scope = iota
	scopeEntry        // a catalogue entry's fields
)

var (
	fieldNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
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

// compileField checks fd against the schema and the fields above it (known).
func compileField(fd fieldDef, sc *Schema, sp scope, known map[string]*field) (*field, error) {
	if !fieldNameRe.MatchString(fd.Name) {
		return nil, fmt.Errorf("name must be lower-case letters, digits or '_', starting with a letter")
	}
	if fd.Name == "schema" {
		return nil, fmt.Errorf("\"schema\" is set on every record and cannot be a field")
	}
	if known[fd.Name] != nil {
		return nil, fmt.Errorf("defined twice")
	}
	types := 0
	for _, given := range []bool{fd.Value != nil, fd.Template != nil, fd.Ref != nil, fd.Pick != nil, fd.Int != nil,
		fd.Money != nil, fd.Date != nil, fd.Text != nil, fd.Lines != nil, fd.Sum != nil, fd.Percent != nil, fd.Nth != nil} {
		if given {
			types++
		}
	}
	if types != 1 {
		return nil, fmt.Errorf("needs exactly one type key (value, template, ref, pick, int, money, date, text, lines, sum, percent or nth)")
	}
	f := &field{name: fd.Name, format: fd.Format}
	var err error
	switch {
	case fd.Value != nil:
		f.kind, f.ref = kindValue, *fd.Value
		err = checkName(f.ref, sc, sp, known)
	case fd.Template != nil:
		f.kind = kindTemplate
		f.template, err = compileTemplate(*fd.Template, sc, sp, known)
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
		err = f.compilePick(fd.Pick, sc)
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
		if sp == scopeEntry {
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
	case fd.Sum != nil:
		f.kind = kindSum
		err = f.compileSum(fd.Sum, known)
	case fd.Nth != nil:
		// A catalogue entry's item of a list, in order, so entries do not
		// repeat one; parseSchema checks the catalogue is no longer.
		if sp != scopeEntry {
			return nil, fmt.Errorf("nth is only for catalogue entries")
		}
		f.kind = kindNth
		err = f.compilePick(*fd.Nth, sc)
		if err == nil && f.listName == "" {
			err = fmt.Errorf("nth takes \"@name\", a list under [lists]")
		}
	case fd.Percent != nil:
		f.kind, f.of, f.rate = kindPercent, fd.Percent.Of, fd.Percent.Rate
		if g := known[f.of]; g == nil || !g.isMoney() {
			err = fmt.Errorf("percent of %q: not a money field above this one", f.of)
		} else if f.rate < 0 || f.rate > 1000 {
			err = fmt.Errorf("percent rate must be 0-1000")
		}
	}
	if err != nil {
		return nil, err
	}
	return f, f.checkFormat()
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
		if f.format != "display" && f.format != "number" {
			return fmt.Errorf("format must be display or number")
		}
	case f.kind == kindDate:
		if f.format == "" {
			f.format = "2006-01-02"
		}
	case f.format != "":
		return fmt.Errorf("format is only for money and date fields")
	}
	return nil
}

func (f *field) compilePick(v any, sc *Schema) error {
	switch p := v.(type) {
	case string:
		name, ok := strings.CutPrefix(p, "@")
		if !ok {
			return fmt.Errorf("pick takes a list, or \"@name\" for a list under [lists]")
		}
		for _, profile := range Profiles {
			if len(sc.list(profile, name)) == 0 {
				return fmt.Errorf("list %q is missing for profile %s (or put it under lists.all)", name, profile)
			}
		}
		f.listName = name
	case []any:
		for _, item := range p {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("pick lists hold strings")
			}
			f.list = append(f.list, s)
		}
		if len(f.list) == 0 {
			return fmt.Errorf("pick list is empty")
		}
	default:
		return fmt.Errorf("pick takes a list of strings or \"@name\"")
	}
	return nil
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
		if lines, column, ok := strings.Cut(n, "."); ok {
			// The column is checked once the catalogue is known; see
			// checkCatalogues. qty is not money.
			if g := known[lines]; g == nil || g.kind != kindLines || len(names) != 1 || column == "" || column == "qty" {
				return fmt.Errorf("sum %q: a lines field's money column, on its own", n)
			}
			continue
		}
		if g := known[n]; g == nil || !g.isMoney() {
			return fmt.Errorf("sum %q: not a money field above this one", n)
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
// record's person or company, or a field above this one.
func checkName(name string, sc *Schema, sp scope, known map[string]*field) error {
	if known[name] != nil {
		return nil
	}
	head, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		return fmt.Errorf("%q is not a field above this one", name)
	}
	if sp == scopeEntry || sc.Step == StepCatalogue {
		return fmt.Errorf("%q: catalogue records are about no person or company", name)
	}
	attr, sub, _ := strings.Cut(rest, ".")
	attrs := personAttrs
	if head == "company" {
		attrs = companyAttr
	} else if head != "person" {
		return fmt.Errorf("%q: names are person.*, company.* or a field above this one", name)
	}
	if !attrs[attr] || (sub != "" && (attr != "address" || !addressAttr[sub])) {
		return fmt.Errorf("%q: unknown attribute", name)
	}
	return nil
}

// tmplTok is literal text, a name to read, or a text generator to call.
type tmplTok struct {
	literal string
	name    string
	text    string
}

var placeholderRe = regexp.MustCompile(`\{([a-z_.]+)\}`)

// compileTemplate parses "{label} - {company.short}": names to read, and
// text generators ({label}, {title}, {subject}, {sentence}) that draw like a
// text field. A title is filled after the fields, so its draws come last.
func compileTemplate(s string, sc *Schema, sp scope, known map[string]*field) ([]tmplTok, error) {
	var toks []tmplTok
	last := 0
	for _, m := range placeholderRe.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			toks = append(toks, tmplTok{literal: s[last:m[0]]})
		}
		name := s[m[2]:m[3]]
		if textKinds[name] && known[name] == nil {
			toks = append(toks, tmplTok{text: name})
		} else {
			if err := checkName(name, sc, sp, known); err != nil {
				return nil, err
			}
			toks = append(toks, tmplTok{name: name})
		}
		last = m[1]
	}
	if last < len(s) {
		toks = append(toks, tmplTok{literal: s[last:]})
	}
	if strings.ContainsAny(strings.Join(literals(toks), ""), "{}") {
		return nil, fmt.Errorf("template %q has a malformed placeholder", s)
	}
	return toks, nil
}

func literals(toks []tmplTok) []string {
	var out []string
	for _, t := range toks {
		out = append(out, t.literal)
	}
	return out
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

// evalContext is what one record is about.
type evalContext struct {
	g       *Gen
	profile Profile
	schema  *Schema
	person  Person
	company Company
	people  []Person // the request's people, for company.contacts
	entryN  int      // the catalogue entry being made
	vals    map[string]val
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
	c := &evalContext{g: g, profile: in.Profile, schema: s, people: in.People, vals: map[string]val{}}
	out := map[string]any{"schema": s.Name}
	switch s.Step {
	case StepCatalogue:
		for name, v := range s.entry(in.Profile, index%s.catalogue.size) {
			c.vals[name], out[name] = v, v.json
		}
	default:
		if len(in.People) == 0 {
			return Record{}, fmt.Errorf("a record needs at least one person")
		}
		slots := max(in.Slots, 1)
		c.person = in.People[personIndex(in.Slot, slots, index, len(in.People))]
		if s.Step == StepCompany {
			c.person = companyPerson(in.People, index)
		}
		c.company = CompanyFor(c.person.Company)
	}
	for _, f := range s.fields {
		v, err := c.eval(f)
		if err != nil {
			return Record{}, fmt.Errorf("record schema %s, field %s: %w", s.Name, f.name, err)
		}
		c.vals[f.name], out[f.name] = v, v.json
	}
	rec := Record{Schema: s.Name, Title: c.template(s.titleToks), Data: mustJSON(out)}
	for _, l := range s.links {
		for _, id := range c.linked(l.value) {
			rec.People = append(rec.People, PersonRole{id, l.role})
		}
	}
	return rec, nil
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
func (s *Schema) entry(profile Profile, n int) map[string]val {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "catalogue\x00%s\x00%s\x00%d", s.Name, profile, n)
	c := &evalContext{g: New(h.Sum64()), profile: profile, schema: s, entryN: n, vals: map[string]val{}}
	for _, f := range s.catalogue.fields {
		v, err := c.eval(f)
		if err != nil {
			// Entry fields are checked at load and draw only from their
			// own generator; an error is a broken field type.
			panic(fmt.Sprintf("catalogue %s entry %d, field %s: %v", s.Name, n, f.name, err))
		}
		c.vals[f.name] = v
	}
	return c.vals
}

func (c *evalContext) eval(f *field) (val, error) {
	g := c.g
	switch f.kind {
	case kindValue:
		return c.read(f.ref), nil
	case kindTemplate:
		s := c.template(f.template)
		return val{json: s, text: s}, nil
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
			list = c.schema.list(c.profile, f.listName)
		}
		s := g.pick(list)
		return val{json: s, text: s}, nil
	case kindInt:
		n := g.Between(f.lo, f.hi)
		return val{json: n, text: fmt.Sprint(n)}, nil
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
		return c.lines(f), nil
	case kindSum:
		var cents int64
		if lines, column, ok := strings.Cut(f.sums[0], "."); ok {
			for _, row := range c.vals[lines].rows {
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
		s := list[c.entryN%len(list)]
		return val{json: s, text: s}, nil
	}
	return val{}, fmt.Errorf("unknown field type")
}

// lines picks distinct catalogue entries, each with a quantity and the
// amount it comes to.
func (c *evalContext) lines(f *field) val {
	cat := f.catalogueSchema
	n := min(c.g.Between(f.lo, f.hi), cat.catalogue.size)
	used := map[int]bool{}
	price := cat.catalogueField("unit_price")
	var rows []map[string]val
	var out []map[string]any
	for len(rows) < n {
		i := c.g.Intn(cat.catalogue.size)
		if used[i] {
			continue
		}
		used[i] = true
		row := cat.entry(c.profile, i)
		qty := c.g.Between(f.qlo, f.qhi)
		row["qty"] = val{json: qty, text: fmt.Sprint(qty)}
		row["amount"] = moneyVal(row["unit_price"].cents*int64(qty), price.format)
		js := map[string]any{}
		for k, v := range row {
			js[k] = v.json
		}
		rows, out = append(rows, row), append(out, js)
	}
	return val{json: out, rows: rows}
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

func (c *evalContext) template(toks []tmplTok) string {
	var b strings.Builder
	for _, t := range toks {
		switch {
		case t.text != "":
			b.WriteString(c.text(t.text, 0, 0))
		case t.name != "":
			b.WriteString(c.read(t.name).text)
		default:
			b.WriteString(t.literal)
		}
	}
	return b.String()
}

// read returns a person or company attribute, or an earlier field.
func (c *evalContext) read(name string) val {
	if v, ok := c.vals[name]; ok {
		return v
	}
	head, rest, _ := strings.Cut(name, ".")
	attr, sub, _ := strings.Cut(rest, ".")
	p, co := c.person, c.company
	var v any
	switch head + "." + attr {
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
		v = addressVal(co.Address, sub)
	case "company.contacts":
		ids := c.linked("company.contacts")
		return val{json: ids, text: strings.Join(ids, ", ")}
	}
	if a, ok := v.(Address); ok {
		return val{json: a, text: a.Street + ", " + a.Suburb + " " + a.State + " " + a.Postcode}
	}
	s, _ := v.(string)
	return val{json: v, text: s}
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

// linked returns the person IDs a link value names.
func (c *evalContext) linked(value string) []string {
	if value == "person.id" {
		return []string{c.person.ID}
	}
	var ids []string
	for _, q := range c.people {
		if q.Company == c.person.Company {
			ids = append(ids, q.ID)
		}
	}
	return ids
}

// moneyVal shows cents as "$1,234.56" (display) or 1234.56 (number).
func moneyVal(cents int64, format string) val {
	display := formatMoney(int(cents))
	v := val{cents: cents, text: display, json: display}
	if format == "number" {
		v.json = json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100))
	}
	return v
}
