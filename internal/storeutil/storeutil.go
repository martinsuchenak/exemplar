// Package storeutil holds small helpers shared by the storage layers: IDs,
// timestamp and JSON column encoding, and constraint error detection.
package storeutil

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
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
