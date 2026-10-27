// Package principal describes the authenticated caller that every service
// method receives. It has no dependencies on the services themselves, so any
// service can check permissions without import cycles.
package principal

import (
	"context"
	"slices"

	"github.com/martinsuchenak/exemplar/internal/ctxkeys"
)

// Role is an account's role.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleMember || r == RoleAdmin
}

// Scope is a permission carried by an API key.
type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
	ScopeAdmin Scope = "admin"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool {
	return s == ScopeRead || s == ScopeWrite || s == ScopeAdmin
}

// Principal is an account acting through one API key.
type Principal struct {
	AccountID   string  `json:"account_id"`
	AccountName string  `json:"account_name"`
	Role        Role    `json:"role"`
	KeyID       string  `json:"key_id"`
	Scopes      []Scope `json:"scopes"`
	// NoQuarantine keeps quarantine datasets and their fixtures from this
	// request, whatever the key may see. The entry point sets it, not the
	// key: the MCP endpoint does, so agents read only reviewed data that has
	// been promoted out of quarantine.
	NoQuarantine bool `json:"-"`
}

// Has reports whether the key carries scope s itself.
func (p Principal) Has(s Scope) bool {
	return slices.Contains(p.Scopes, s)
}

// CanRead reports whether the key may read data at all.
func (p Principal) CanRead() bool {
	return p.Has(ScopeRead) || p.Has(ScopeWrite) || p.Has(ScopeAdmin)
}

// CanWrite reports whether the key may change data.
func (p Principal) CanWrite() bool {
	return p.Has(ScopeWrite) || p.Has(ScopeAdmin)
}

// IsAdmin needs both an admin account and a key carrying the admin scope, so
// an admin's everyday read/write key cannot approve or manage accounts.
func (p Principal) IsAdmin() bool {
	return p.Role == RoleAdmin && p.Has(ScopeAdmin)
}

// CanReadQuarantine reports whether the request may read quarantine: an
// admin, through an entry point that serves it.
func (p Principal) CanReadQuarantine() bool {
	return p.IsAdmin() && !p.NoQuarantine
}

// WithContext returns ctx carrying p.
func WithContext(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxkeys.PrincipalKey, p)
}

// FromContext returns the principal stored by WithContext.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxkeys.PrincipalKey).(Principal)
	return p, ok
}
