// Package anonymisetest gives tests outside the anonymiser a stand-in for
// Presidio and a few small real-looking emails.
package anonymisetest

import (
	"context"
	"fmt"
	"strings"

	"github.com/martinsuchenak/exemplar/internal/anonymise"
)

// Key is a valid anonymisation secret for tests.
var Key = []byte("exemplar-test-anonymisation-key")

// Detector finds the given names (and the names the engine passes) as PERSON,
// the way Presidio's NER would.
type Detector struct {
	Names []string
}

// Detect implements anonymise.Detector.
func (d Detector) Detect(_ context.Context, text string, extra []string, _ float64) ([]anonymise.Span, error) {
	var spans []anonymise.Span
	for _, n := range append(append([]string{}, d.Names...), extra...) {
		if n == "" {
			continue
		}
		for i := 0; ; {
			j := strings.Index(text[i:], n)
			if j < 0 {
				break
			}
			spans = append(spans, anonymise.Span{Start: i + j, End: i + j + len(n), Entity: anonymise.EntityPerson})
			i += j + len(n)
		}
	}
	return spans, nil
}

// Email builds a plain-text email from one named person to another; names are
// "First Last" and addresses are first.last@domain.
func Email(from, to, domain, subject, body string) []byte {
	addr := func(name string) string {
		return fmt.Sprintf("%s <%s@%s>", name, strings.ToLower(strings.ReplaceAll(name, " ", ".")), domain)
	}
	return []byte("From: " + addr(from) + "\r\nTo: " + addr(to) + "\r\nSubject: " + subject +
		"\r\nDate: Mon, 6 Jan 2025 09:30:00 +1100\r\nMessage-ID: <" + strings.ToLower(strings.ReplaceAll(subject, " ", "-")) + "@" + domain +
		">\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}
