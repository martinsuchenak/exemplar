// Package storeutil holds small helpers shared by the storage layers: IDs,
// timestamp and JSON column encoding, constraint error detection and word
// search.
package storeutil

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/martinsuchenak/exemplar/internal/apperr"
)

// idRe matches the IDs exemplar makes (UUIDv7) and anything close enough to
// be safe in a URL path segment and as a key: letters, digits and '-'.
var idRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// ValidID reports whether id may be stored as an ID. Records that arrive with
// their own IDs (imports) must pass it, so no ID carries path characters
// into API URLs.
func ValidID(id string) bool {
	return idRe.MatchString(id)
}

// NewID returns a UUIDv7, which sorts by creation time.
func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// FormatTime stores times as RFC 3339 UTC with nanoseconds, so they sort as text.
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// ParseTime parses a FormatTime value; invalid input gives the zero time.
func ParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// NullTime converts a nullable timestamp column.
func NullTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := ParseTime(s.String)
	return &t
}

// TimeArg converts an optional time to a column value.
func TimeArg(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return FormatTime(*t)
}

// EncodeStrings stores a string list as a JSON array column.
func EncodeStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// DecodeStrings reads an EncodeStrings column; invalid input gives an empty list.
func DecodeStrings(s string) []string {
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil || v == nil {
		return []string{}
	}
	return v
}

// IsUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint failure.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// IsForeignKeyViolation reports whether err is a FOREIGN KEY constraint failure.
func IsForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// Limits on a search query, so one cannot build an arbitrarily long SQL
// condition.
const (
	maxSearchLen   = 200
	maxSearchTerms = 8
)

// SearchTerms splits a search query into the words that must each appear:
// whitespace and the punctuation names use between words (- _ . / ,)
// separate them, so "crm contacts" finds "crm-contacts". An empty query gives
// no terms; one too long gives apperr.ErrInvalid.
func SearchTerms(query string) ([]string, error) {
	if len(query) > maxSearchLen {
		return nil, fmt.Errorf("%w: query is longer than %d characters", apperr.ErrInvalid, maxSearchLen)
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("-_./,", r)
	})
	if len(terms) > maxSearchTerms {
		return nil, fmt.Errorf("%w: query has more than %d words", apperr.ErrInvalid, maxSearchTerms)
	}
	return terms, nil
}

// ContainsPattern returns a LIKE pattern matching s anywhere in a value, with
// LIKE's wildcards in s escaped; use it with ESCAPE '\'. SQLite's LIKE
// ignores the case of ASCII letters only.
func ContainsPattern(s string) string {
	return "%" + likeEscaper.Replace(s) + "%"
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
