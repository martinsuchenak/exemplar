package principal

import (
	"context"
	"testing"
)

func TestPermissions(t *testing.T) {
	tests := []struct {
		name                   string
		p                      Principal
		read, write, admin     bool
		hasRead, hasAdminScope bool
	}{
		{"read only", Principal{Role: RoleMember, Scopes: []Scope{ScopeRead}}, true, false, false, true, false},
		{"read write", Principal{Role: RoleMember, Scopes: []Scope{ScopeRead, ScopeWrite}}, true, true, false, true, false},
		{"write implies read", Principal{Role: RoleMember, Scopes: []Scope{ScopeWrite}}, true, true, false, false, false},
		{"admin account, rw key", Principal{Role: RoleAdmin, Scopes: []Scope{ScopeRead, ScopeWrite}}, true, true, false, true, false},
		{"admin account, admin key", Principal{Role: RoleAdmin, Scopes: []Scope{ScopeAdmin}}, true, true, true, false, true},
		{"admin scope on member", Principal{Role: RoleMember, Scopes: []Scope{ScopeAdmin}}, true, true, false, false, true},
		{"no scopes", Principal{Role: RoleAdmin}, false, false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.CanRead(); got != tt.read {
				t.Errorf("CanRead = %v, want %v", got, tt.read)
			}
			if got := tt.p.CanWrite(); got != tt.write {
				t.Errorf("CanWrite = %v, want %v", got, tt.write)
			}
			if got := tt.p.IsAdmin(); got != tt.admin {
				t.Errorf("IsAdmin = %v, want %v", got, tt.admin)
			}
			if got := tt.p.Has(ScopeRead); got != tt.hasRead {
				t.Errorf("Has(read) = %v, want %v", got, tt.hasRead)
			}
			if got := tt.p.Has(ScopeAdmin); got != tt.hasAdminScope {
				t.Errorf("Has(admin) = %v, want %v", got, tt.hasAdminScope)
			}
			if got := tt.p.CanReadQuarantine(); got != tt.admin {
				t.Errorf("CanReadQuarantine = %v, want %v", got, tt.admin)
			}
			hidden := tt.p
			hidden.NoQuarantine = true
			if hidden.CanReadQuarantine() {
				t.Error("CanReadQuarantine with NoQuarantine")
			}
		})
	}
}

func TestValidValues(t *testing.T) {
	for _, r := range []Role{RoleMember, RoleAdmin} {
		if !r.Valid() {
			t.Errorf("%s should be valid", r)
		}
	}
	if Role("owner").Valid() {
		t.Error("unknown role is valid")
	}
	for _, s := range []Scope{ScopeRead, ScopeWrite, ScopeAdmin} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if Scope("delete").Valid() {
		t.Error("unknown scope is valid")
	}
}

func TestContextRoundTrip(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Error("empty context returned a principal")
	}
	p := Principal{AccountID: "a1", KeyID: "k1"}
	got, ok := FromContext(WithContext(context.Background(), p))
	if !ok || got.AccountID != "a1" || got.KeyID != "k1" {
		t.Errorf("FromContext = %+v, %v", got, ok)
	}
}
