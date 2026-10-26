// Package synth generates fake but realistic test content: people, companies,
// business text, files (PDF, DOCX, XLSX, CSV, TXT, RTF, HTML, images), emails
// in many shapes, and records. It is pure: no database, no network, no clock.
// Everything comes from a seeded generator, so the same seed and inputs give
// the same bytes, and every address is on a reserved .example domain.
package synth

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"time"
	"unicode"
)

// Gen is a seeded source of fake content. It is not safe for concurrent use.
type Gen struct {
	r *rand.Rand
	// base is the reference time generated dates are spread around, so the
	// output does not depend on when it runs.
	base time.Time
}

// New returns a generator for seed.
func New(seed uint64) *Gen {
	return &Gen{
		r:    rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		base: time.Date(2026, 1, 15, 9, 0, 0, 0, time.FixedZone("AEST", 10*3600)),
	}
}

// Intn returns a number in [0, n).
func (g *Gen) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return g.r.IntN(n)
}

// Between returns a number in [lo, hi].
func (g *Gen) Between(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + g.r.IntN(hi-lo+1)
}

// Chance reports true with probability 1 in n.
func (g *Gen) Chance(n int) bool {
	return g.Intn(n) == 0
}

func (g *Gen) pick(s []string) string {
	return s[g.Intn(len(s))]
}

// Date returns a time within days of the generator's base date.
func (g *Gen) Date(days int) time.Time {
	offset := time.Duration(g.Between(-days*24*60, days*24*60)) * time.Minute
	return g.base.Add(offset).Truncate(time.Minute)
}

// Address is a postal address.
type Address struct {
	Street   string `json:"street"`
	Suburb   string `json:"suburb"`
	State    string `json:"state"`
	Postcode string `json:"postcode"`
	Country  string `json:"country"`
}

// String formats the address on one line.
func (a Address) String() string {
	return fmt.Sprintf("%s, %s %s %s", a.Street, a.Suburb, a.State, a.Postcode)
}

// Person is a fake person. ID is empty until the person registry stores it.
type Person struct {
	ID        string  `json:"id,omitempty"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	Email     string  `json:"email"`
	Phone     string  `json:"phone"`
	JobTitle  string  `json:"job_title"`
	Company   string  `json:"company"`
	Address   Address `json:"address"`
}

// Name is "First Last".
func (p Person) Name() string {
	return p.FirstName + " " + p.LastName
}

// Company is a fake company. Its details are derived from its name, so the
// same company always has the same domain, ABN, phone and address.
type Company struct {
	Name     string  `json:"name"`
	Domain   string  `json:"domain"`
	ABN      string  `json:"abn"`
	Phone    string  `json:"phone"`
	Industry string  `json:"industry"`
	Website  string  `json:"website"`
	Address  Address `json:"address"`
}

// CompanyName returns a new company name such as "Harlow Civil Engineering Pty Ltd".
func (g *Gen) CompanyName() string {
	stems := companyStems
	if g.Chance(3) {
		stems = companyPlaces
	}
	return g.pick(stems) + " " + g.pick(companyTrades) + " " + g.pick(companySuffixes)
}

// CompanyFor returns the details of the company called name. The result
// depends only on name, not on the generator's state.
func CompanyFor(name string) Company {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	g := New(h.Sum64())
	slug := slugify(trimSuffix(name))
	industry := "General"
	for _, t := range companyTrades {
		if strings.Contains(name, " "+t+" ") || strings.HasSuffix(name, " "+t) {
			industry = t
			break
		}
	}
	return Company{
		Name:     name,
		Domain:   slug + ".example",
		ABN:      g.abn(),
		Phone:    g.landline(),
		Industry: industry,
		Website:  "https://www." + slug + ".example",
		Address:  g.Address(),
	}
}

// Person returns a new person working at company (a name from CompanyName).
// Their email is first.last@<company domain>.
func (g *Gen) Person(company string) Person {
	first, last := g.pick(firstNames), g.pick(lastNames)
	return Person{
		FirstName: first,
		LastName:  last,
		Email:     emailLocal(first, last) + "@" + CompanyFor(company).Domain,
		Phone:     g.mobile(),
		JobTitle:  g.pick(jobTitles),
		Company:   company,
		Address:   g.Address(),
	}
}

// Address returns a new Australian-style address.
func (g *Gen) Address() Address {
	s := suburbs[g.Intn(len(suburbs))]
	street := fmt.Sprintf("%d %s %s", g.Between(1, 240), g.pick(streetNames), g.pick(streetTypes))
	if g.Chance(4) {
		street = fmt.Sprintf("Unit %d, %s", g.Between(1, 40), street)
	}
	return Address{Street: street, Suburb: s.name, State: s.state, Postcode: s.postcode, Country: "Australia"}
}

func (g *Gen) mobile() string {
	return fmt.Sprintf("04%02d %03d %03d", g.Intn(100), g.Intn(1000), g.Intn(1000))
}

func (g *Gen) landline() string {
	return fmt.Sprintf("(0%d) %04d %04d", []int{2, 3, 7, 8}[g.Intn(4)], g.Intn(10000), g.Intn(10000))
}

func (g *Gen) abn() string {
	return fmt.Sprintf("%02d %03d %03d %03d", g.Between(10, 99), g.Intn(1000), g.Intn(1000), g.Intn(1000))
}

// emailLocal turns a name into an address local part: "O'Brien" -> "obrien".
func emailLocal(first, last string) string {
	return slugDots(first) + "." + slugDots(last)
}

func slugDots(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Slug turns a title into a lower-case, hyphenated file-name stem of at most
// 48 characters.
func Slug(s string) string {
	slug := slugify(s)
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	if slug == "" {
		return "item"
	}
	return slug
}

// slugify turns a name into a lower-case, hyphenated domain label.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) && r < unicode.MaxASCII || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// trimSuffix removes the longest company suffix, so "Harlow Co Pty Ltd"
// becomes "Harlow", not "Harlow Co".
func trimSuffix(name string) string {
	longest := ""
	for _, s := range companySuffixes {
		if strings.HasSuffix(name, " "+s) && len(s) > len(longest) {
			longest = s
		}
	}
	return strings.TrimSuffix(name, " "+longest)
}

var jobTitles = []string{
	"Account Manager", "Operations Manager", "Office Manager", "Project Manager", "Finance Officer",
	"Claims Officer", "Senior Broker", "Underwriter", "Sales Coordinator", "Workshop Supervisor",
	"Estimator", "Procurement Officer", "General Manager", "Customer Service Officer", "Director",
	"Administration Officer", "Quality Manager", "Site Manager", "Accounts Payable Officer", "Team Leader",
}

var streetNames = []string{
	"Acacia", "Banksia", "Beach", "Bridge", "Church", "Station", "High", "King", "Queen", "Victoria",
	"George", "Elizabeth", "Albert", "Railway", "Park", "Hill", "Mill", "Wattle", "Jacaranda", "Ocean",
	"River", "Forest", "Harbour", "Market", "Smith", "Pacific", "Gordon", "Ridge", "Lake", "Coronation",
}

var streetTypes = []string{"Street", "Road", "Avenue", "Drive", "Parade", "Lane", "Place", "Crescent", "Way", "Terrace"}

type suburb struct{ name, state, postcode string }

var suburbs = []suburb{
	{"Parramatta", "NSW", "2150"}, {"Newtown", "NSW", "2042"}, {"Chatswood", "NSW", "2067"},
	{"Penrith", "NSW", "2750"}, {"Wollongong", "NSW", "2500"}, {"Newcastle", "NSW", "2300"},
	{"Fitzroy", "VIC", "3065"}, {"Richmond", "VIC", "3121"}, {"Geelong", "VIC", "3220"},
	{"Ballarat", "VIC", "3350"}, {"Dandenong", "VIC", "3175"}, {"Footscray", "VIC", "3011"},
	{"Fortitude Valley", "QLD", "4006"}, {"Southport", "QLD", "4215"}, {"Toowoomba", "QLD", "4350"},
	{"Townsville", "QLD", "4810"}, {"Cairns", "QLD", "4870"}, {"Ipswich", "QLD", "4305"},
	{"Fremantle", "WA", "6160"}, {"Joondalup", "WA", "6027"}, {"Bunbury", "WA", "6230"},
	{"Subiaco", "WA", "6008"}, {"Glenelg", "SA", "5045"}, {"Norwood", "SA", "5067"},
	{"Mount Gambier", "SA", "5290"}, {"Sandy Bay", "TAS", "7005"}, {"Launceston", "TAS", "7250"},
	{"Belconnen", "ACT", "2617"}, {"Braddon", "ACT", "2612"}, {"Palmerston", "NT", "0830"},
}
