package anonymise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Entity types the anonymiser handles.
const (
	EntityPerson       = "PERSON"
	EntityEmail        = "EMAIL_ADDRESS"
	EntityPhone        = "PHONE_NUMBER"
	EntityURL          = "URL"
	EntityCreditCard   = "CREDIT_CARD"
	EntityIBAN         = "IBAN_CODE"
	EntityIP           = "IP_ADDRESS"
	EntityABN          = "AU_ABN"
	EntityACN          = "AU_ACN"
	EntityTFN          = "AU_TFN"
	EntityMedicare     = "AU_MEDICARE"
	EntityOrganization = "ORGANIZATION"
	EntityCompany      = "COMPANY"
	// EntityTerm is a profile term (Profile.Terms).
	EntityTerm      = "PROFILE_TERM"
	EntityMoney     = "MONEY"
	EntityStreet    = "STREET_ADDRESS"
	EntityDomain    = "DOMAIN"
	EntityContentID = "CONTENT_ID"
	// EntityBankAccount is a bank account number or a BSB, found by its
	// label (Presidio has no recogniser for them).
	EntityBankAccount = "BANK_ACCOUNT"
)

// presidioEntities are requested from Presidio; the Go patterns cover the rest.
var presidioEntities = []string{
	EntityPerson, EntityEmail, EntityPhone, EntityURL, EntityCreditCard, EntityIBAN, EntityIP,
	EntityABN, EntityACN, EntityTFN, EntityMedicare, EntityOrganization,
}

// digitEntities are replaced digit by digit, keeping their format.
var digitEntities = map[string]bool{
	EntityPhone: true, EntityCreditCard: true, EntityIBAN: true, EntityABN: true, EntityACN: true,
	EntityTFN: true, EntityMedicare: true, EntityMoney: true, EntityIP: true, EntityBankAccount: true,
}

// priority decides between two types found for the same text: the more
// specific wins (an email over a URL, a company over an organisation).
var priority = map[string]int{
	EntityContentID: 10, EntityEmail: 9, EntityURL: 8, EntityCompany: 7, EntityStreet: 7, EntityABN: 6,
	EntityBankAccount: 6, EntityPhone: 6, EntityMoney: 5, EntityPerson: 4, EntityOrganization: 3,
}

// Span is one detected value in a text, as byte offsets.
type Span struct {
	Start, End int
	Entity     string
}

// Detector finds personal data in text. names lists people already known
// from the headers, which should be found wherever they appear.
type Detector interface {
	Detect(ctx context.Context, text string, names []string, threshold float64) ([]Span, error)
}

// ErrDetector marks every error from the detector: Presidio unreachable, an
// error status from it, or an answer that cannot be read. Its detail names
// the service's URL, host and network, so a server shows its callers
// DetectorUnavailable instead (see PublicError); locally it is shown whole.
var ErrDetector = errors.New("the detector failed")

// DetectorUnavailable is all a server's caller learns of a detector failure.
const DetectorUnavailable = "the anonymiser's detector is unavailable; try again later"

// DetectorFailure describes a detector failure for a server's log: what
// failed (the status Presidio returned, the network error, the timeout),
// without what Presidio said back, which could quote the text it was sent.
func DetectorFailure(err error) string {
	var st *presidioStatus
	if errors.As(err, &st) {
		return "presidio returned " + st.status
	}
	return err.Error()
}

// presidioStatus is an error status from Presidio, with what it said; the
// person running the tool locally sees both.
type presidioStatus struct {
	status, body string
}

func (e *presidioStatus) Error() string {
	return fmt.Sprintf("presidio returned %s: %s", e.status, e.body)
}

// PublicError is err's message for a server's caller: DetectorUnavailable
// for a detector failure, err's own message otherwise.
func PublicError(err error) string {
	if errors.Is(err, ErrDetector) {
		return DetectorUnavailable
	}
	return err.Error()
}

// Presidio is a Detector backed by a Presidio Analyzer service.
type Presidio struct {
	URL  string
	HTTP *http.Client
}

// NewPresidio returns a client for the analyzer at url (e.g. http://localhost:3000).
func NewPresidio(url string) *Presidio {
	return &Presidio{URL: strings.TrimRight(url, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

type presidioRecognizer struct {
	Name              string   `json:"name"`
	SupportedLanguage string   `json:"supported_language"`
	SupportedEntity   string   `json:"supported_entity"`
	DenyList          []string `json:"deny_list"`
}

type presidioRequest struct {
	Text           string               `json:"text"`
	Language       string               `json:"language"`
	Entities       []string             `json:"entities"`
	ScoreThreshold float64              `json:"score_threshold"`
	AdHoc          []presidioRecognizer `json:"ad_hoc_recognizers,omitempty"`
}

type presidioResult struct {
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

// maxChunk keeps each request well under spaCy's one-million-character limit.
const maxChunk = 200_000

// Detect asks Presidio for entities. Presidio counts characters (code
// points); the spans are converted to byte offsets.
func (p *Presidio) Detect(ctx context.Context, text string, names []string, threshold float64) ([]Span, error) {
	runes := []rune(text)
	var spans []Span
	for start := 0; start < len(runes); start += maxChunk {
		end := min(start+maxChunk, len(runes))
		chunk := string(runes[start:end])
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		res, err := p.analyze(ctx, chunk, names, threshold)
		if err != nil {
			return nil, err
		}
		offsets := byteOffsets(runes, start)
		for _, r := range res {
			if r.Start < 0 || r.End > end-start || r.Start >= r.End {
				continue
			}
			spans = append(spans, Span{Start: offsets(start + r.Start), End: offsets(start + r.End), Entity: r.EntityType})
		}
	}
	return spans, nil
}

func (p *Presidio) analyze(ctx context.Context, text string, names []string, threshold float64) ([]presidioResult, error) {
	body := presidioRequest{Text: text, Language: "en", Entities: presidioEntities, ScoreThreshold: threshold}
	if len(names) > 0 {
		body.AdHoc = []presidioRecognizer{{Name: "exemplar_header_names", SupportedLanguage: "en", SupportedEntity: EntityPerson, DenyList: names}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/analyze", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Presidio: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &presidioStatus{status: resp.Status, body: strings.TrimSpace(string(msg))}
	}
	var out []presidioResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding Presidio response: %w", err)
	}
	return out, nil
}

// byteOffsets returns a function turning a rune index into a byte offset.
func byteOffsets(runes []rune, _ int) func(int) int {
	offsets := make([]int, len(runes)+1)
	n := 0
	for i, r := range runes {
		offsets[i] = n
		n += len(string(r))
	}
	offsets[len(runes)] = n
	return func(i int) int { return offsets[i] }
}

// patterns are the Go recognisers, run on every text in addition to Presidio:
// Australian formats Presidio does not cover by default, and the formats the
// rest of the anonymiser relies on.
var patterns = []struct {
	entity string
	re     *regexp.Regexp
	group  int
	// check, when set, must accept a match for it to count.
	check func(string) bool
}{
	{EntityEmail, regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`), 0, nil},
	{EntityURL, regexp.MustCompile(`(?i)\bhttps?://[^\s<>"')\]]+`), 0, nil},
	{EntityURL, regexp.MustCompile(`(?i)\bwww\.[a-z0-9-]+(?:\.[a-z0-9-]+)+[^\s<>"')\]]*`), 0, nil},
	{EntityMoney, regexp.MustCompile(`(?:AUD|USD|NZD|EUR|GBP|A\$|US\$|\$|£|€)\s?\d[\d,]*(?:\.\d{1,2})?(?:\s?[kKmM]\b)?`), 0, nil},
	{EntityMoney, regexp.MustCompile(`\b\d[\d,]*\.\d{2}\s?(?:AUD|USD|NZD|EUR|GBP|dollars)\b`), 0, nil},
	{EntityPhone, regexp.MustCompile(`(?:\+?61\s?|\b0)4\d{2}[\s-]?\d{3}[\s-]?\d{3}\b`), 0, nil},
	{EntityPhone, regexp.MustCompile(`(?:\+?61\s?\(?0?|\(?\b0)[2378]\)?[\s-]?\d{4}[\s-]?\d{4}\b`), 0, nil},
	{EntityPhone, regexp.MustCompile(`\b1[38]00[\s-]?\d{3}[\s-]?\d{3}\b`), 0, nil},
	// Any country's number written with its calling code ("+1 469-555-0100",
	// "+213 662 34 59 10"): the "+" says it is a phone number, whatever the
	// country writes it like. 8 to 15 digits, as E.164 allows.
	{EntityPhone, regexp.MustCompile(`\B\+\d[\d ().-]{5,22}\d\b`), 0, phoneDigits(8, 15)},
	{EntityABN, regexp.MustCompile(`(?i)\bABN[:\s]\s*(\d{2}\s?\d{3}\s?\d{3}\s?\d{3})\b`), 1, nil},
	{EntityACN, regexp.MustCompile(`(?i)\bACN[:\s]\s*(\d{3}\s?\d{3}\s?\d{3})\b`), 1, nil},
	// Bank details by their labels: "BSB: 062-000", "Account No. 1234 5678",
	// "Account Number 987 654 321", "A/C #12345678".
	{EntityBankAccount, regexp.MustCompile(`(?i)\bBSB(?:\s*(?:No\.?|Number|#))?\s*[:.]?\s*(\d{3}[\s-]?\d{3})\b`), 1, nil},
	{EntityBankAccount, regexp.MustCompile(`(?i)\b(?:Account|Acct|A/C)\s*(?:No\.?|Number|Num|#)\s*[:.]?\s*(\d(?:[\s-]?\d){4,15})\b`), 1, nil},
	{EntityStreet, regexp.MustCompile(`\b(?:(?:Unit|Suite|Level|Shop)\s+\d+[A-Za-z]?[,/]?\s*)?\d{1,5}[A-Za-z]?(?:-\d{1,5})?\s+(?:[A-Z][a-z]+\s){1,3}(?:Street|St|Road|Rd|Avenue|Ave|Drive|Dr|Court|Ct|Place|Pl|Lane|Ln|Parade|Pde|Highway|Hwy|Boulevard|Blvd|Crescent|Cres|Terrace|Tce|Way|Close|Cl)\b\.?`), 0, nil},
	{EntityStreet, regexp.MustCompile(`\b(?:PO|P\.O\.)\s?Box\s+\d{1,6}\b`), 0, nil},
	{EntityCompany, regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9&'\-]+\s){1,5}(?:Pty\.?\s?Ltd\.?|Limited|Ltd\.?|Inc\.?|LLC|Group|Holdings)\b`), 0, nil},
}

// phoneDigits accepts a phone number with min to max digits.
func phoneDigits(min, max int) func(string) bool {
	return func(v string) bool {
		n := 0
		for _, r := range v {
			if r >= '0' && r <= '9' {
				n++
			}
		}
		return n >= min && n <= max
	}
}

func patternSpans(text string) []Span {
	var spans []Span
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := m[2*p.group], m[2*p.group+1]
			if p.check == nil || p.check(text[start:end]) {
				spans = append(spans, Span{Start: start, End: end, Entity: p.entity})
			}
		}
	}
	return spans
}

// found collects detected values for one message, each with its entity type.
type found struct {
	profile  *Profile
	entities map[string]string
}

func newFound(p *Profile) *found {
	return &found{profile: p, entities: map[string]string{}}
}

// cleanValue trims the space and punctuation around a found value.
func cleanValue(v string) string {
	return strings.Trim(strings.TrimSpace(v), `.,;:()[]<>"'`)
}

func (f *found) add(value, entity string) {
	value = cleanValue(value)
	if len(value) < 2 || f.profile.isStop(value) {
		return
	}
	if entity == EntityPerson || entity == EntityOrganization {
		allStop := true
		for _, w := range strings.Fields(value) {
			if !f.profile.isStop(w) {
				allStop = false
			}
		}
		if allStop {
			return
		}
	}
	if existing, ok := f.entities[value]; !ok || priority[entity] > priority[existing] {
		f.entities[value] = entity
	}
}

// dropFragments removes URL matches that are pieces of email addresses or
// Content-IDs ("tom.ba" from "tom.baker@...").
func (f *found) dropFragments() {
	var addrs []string
	for v, e := range f.entities {
		if e == EntityEmail || e == EntityContentID {
			addrs = append(addrs, strings.ToLower(v))
		}
	}
	for v, e := range f.entities {
		if e != EntityURL {
			continue
		}
		for _, a := range addrs {
			if strings.Contains(a, strings.ToLower(v)) {
				delete(f.entities, v)
				break
			}
		}
	}
}

// trimName drops the words with no letter in them from either end of a
// detected name. The detector can run a name into the number beside it
// ("Sarah Lane 0412" out of "Sarah Lane 0412 345 678"); the name's
// match would then overlap the phone number's and keep it from being
// replaced.
func trimName(v string) string {
	hasLetter := func(s string) bool { return strings.IndexFunc(s, unicode.IsLetter) >= 0 }
	v = strings.TrimSpace(v)
	for i := strings.LastIndexFunc(v, unicode.IsSpace); i >= 0 && !hasLetter(v[i:]); i = strings.LastIndexFunc(v, unicode.IsSpace) {
		v = strings.TrimSpace(v[:i])
	}
	for i := strings.IndexFunc(v, unicode.IsSpace); i >= 0 && !hasLetter(v[:i]); i = strings.IndexFunc(v, unicode.IsSpace) {
		v = strings.TrimSpace(v[i:])
	}
	return v
}

// plausibleName filters NER noise that would damage structure if replaced:
// spans across lines, protocol tokens and all-caps acronyms or keywords.
func plausibleName(v string) bool {
	if strings.ContainsAny(v, "\r\n:@/") {
		return false
	}
	if len(strings.Fields(v)) == 1 && strings.ToUpper(v) == v {
		return false
	}
	return strings.IndexFunc(v, unicode.IsLetter) >= 0
}

var (
	skipBlocks = regexp.MustCompile(`(?is)<(style|script|head)\b.*?</(style|script|head)>`)
	blockTags  = regexp.MustCompile(`(?i)<(br|p|div|tr|li|td|h[1-6])\b[^>]*>`)
	attrValues = regexp.MustCompile(`(?i)\b(?:alt|title|href)\s*=\s*"([^"]*)"`)
	tags       = regexp.MustCompile(`<[^>]*>`)
)

// htmlText returns an HTML body's visible text plus alt/title/href values, for
// detection only (replacement works on the raw HTML).
func htmlText(doc string) string {
	doc = skipBlocks.ReplaceAllString(doc, " ")
	var extra strings.Builder
	for _, m := range attrValues.FindAllStringSubmatch(doc, -1) {
		extra.WriteString("\n" + strings.TrimPrefix(html.UnescapeString(m[1]), "mailto:") + "\n")
	}
	doc = blockTags.ReplaceAllString(doc, "\n")
	doc = tags.ReplaceAllString(doc, " ")
	return html.UnescapeString(doc) + extra.String()
}
