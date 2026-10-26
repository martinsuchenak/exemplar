package anonymise

import (
	"bytes"
	"context"
	"encoding/json"
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
	EntityMoney        = "MONEY"
	EntityStreet       = "STREET_ADDRESS"
	EntityDomain       = "DOMAIN"
	EntityContentID    = "CONTENT_ID"
)

// presidioEntities are requested from Presidio; the Go patterns cover the rest.
var presidioEntities = []string{
	EntityPerson, EntityEmail, EntityPhone, EntityURL, EntityCreditCard, EntityIBAN, EntityIP,
	EntityABN, EntityACN, EntityTFN, EntityMedicare, EntityOrganization,
}

// digitEntities are replaced digit by digit, keeping their format.
var digitEntities = map[string]bool{
	EntityPhone: true, EntityCreditCard: true, EntityIBAN: true, EntityABN: true, EntityACN: true,
	EntityTFN: true, EntityMedicare: true, EntityMoney: true, EntityIP: true,
}

// priority decides between two types found for the same text: the more
// specific wins (an email over a URL, a company over an organisation).
var priority = map[string]int{
	EntityContentID: 10, EntityEmail: 9, EntityURL: 8, EntityCompany: 7, EntityStreet: 7, EntityABN: 6,
	EntityPhone: 6, EntityMoney: 5, EntityPerson: 4, EntityOrganization: 3,
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
		return nil, fmt.Errorf("presidio returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
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
}{
	{EntityEmail, regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`), 0},
	{EntityURL, regexp.MustCompile(`(?i)\bhttps?://[^\s<>"')\]]+`), 0},
	{EntityURL, regexp.MustCompile(`(?i)\bwww\.[a-z0-9-]+(?:\.[a-z0-9-]+)+[^\s<>"')\]]*`), 0},
	{EntityMoney, regexp.MustCompile(`(?:AUD|USD|NZD|EUR|GBP|A\$|US\$|\$|£|€)\s?\d[\d,]*(?:\.\d{1,2})?(?:\s?[kKmM]\b)?`), 0},
	{EntityMoney, regexp.MustCompile(`\b\d[\d,]*\.\d{2}\s?(?:AUD|USD|NZD|EUR|GBP|dollars)\b`), 0},
	{EntityPhone, regexp.MustCompile(`(?:\+?61\s?|\b0)4\d{2}[\s-]?\d{3}[\s-]?\d{3}\b`), 0},
	{EntityPhone, regexp.MustCompile(`(?:\+?61\s?\(?0?|\(?\b0)[2378]\)?[\s-]?\d{4}[\s-]?\d{4}\b`), 0},
	{EntityPhone, regexp.MustCompile(`\b1[38]00[\s-]?\d{3}[\s-]?\d{3}\b`), 0},
	{EntityABN, regexp.MustCompile(`(?i)\bABN[:\s]\s*(\d{2}\s?\d{3}\s?\d{3}\s?\d{3})\b`), 1},
	{EntityACN, regexp.MustCompile(`(?i)\bACN[:\s]\s*(\d{3}\s?\d{3}\s?\d{3})\b`), 1},
	{EntityStreet, regexp.MustCompile(`\b(?:(?:Unit|Suite|Level|Shop)\s+\d+[A-Za-z]?[,/]?\s*)?\d{1,5}[A-Za-z]?(?:-\d{1,5})?\s+(?:[A-Z][a-z]+\s){1,3}(?:Street|St|Road|Rd|Avenue|Ave|Drive|Dr|Court|Ct|Place|Pl|Lane|Ln|Parade|Pde|Highway|Hwy|Boulevard|Blvd|Crescent|Cres|Terrace|Tce|Way|Close|Cl)\b\.?`), 0},
	{EntityStreet, regexp.MustCompile(`\b(?:PO|P\.O\.)\s?Box\s+\d{1,6}\b`), 0},
	{EntityCompany, regexp.MustCompile(`\b(?:[A-Z][A-Za-z0-9&'\-]+\s){1,5}(?:Pty\.?\s?Ltd\.?|Limited|Ltd\.?|Inc\.?|LLC|Group|Holdings)\b`), 0},
}

func patternSpans(text string) []Span {
	var spans []Span
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(text, -1) {
			spans = append(spans, Span{Start: m[2*p.group], End: m[2*p.group+1], Entity: p.entity})
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

func (f *found) add(value, entity string) {
	value = strings.Trim(strings.TrimSpace(value), `.,;:()[]<>"'`)
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
