// Package account manages accounts and their API keys. Every access check for
// accounts and keys happens here; REST, CLI and MCP callers go through Service.
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

// Kind says what an account stands for; it does not change permissions.
type Kind string

const (
	KindDeveloper Kind = "developer"
	KindTeam      Kind = "team"
	KindApp       Kind = "app"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	return k == KindDeveloper || k == KindTeam || k == KindApp
}

// Account is whoever owns data and acts through API keys.
type Account struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       Kind           `json:"kind"`
	Role       principal.Role `json:"role"`
	CreatedAt  time.Time      `json:"created_at"`
	DisabledAt *time.Time     `json:"disabled_at,omitempty"`
}

// APIKey is a key's metadata. The secret is shown once, in IssuedKey, and only
// its SHA-256 hash is stored.
type APIKey struct {
	ID         string            `json:"id"`
	AccountID  string            `json:"account_id"`
	Name       string            `json:"name"`
	Prefix     string            `json:"prefix"`
	Scopes     []principal.Scope `json:"scopes"`
	CreatedAt  time.Time         `json:"created_at"`
	LastUsedAt *time.Time        `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time        `json:"revoked_at,omitempty"`
}

// IssuedKey is a newly created key with its secret, which is never stored or shown again.
type IssuedKey struct {
	Key    APIKey `json:"key"`
	Secret string `json:"secret"`
}

// CreateAccountInput describes a new account. Role defaults to member.
type CreateAccountInput struct {
	Name string         `json:"name"`
	Kind Kind           `json:"kind"`
	Role principal.Role `json:"role"`
}

// CreateKeyInput describes a new key. AccountID defaults to the caller's
// account and Scopes to read and write.
type CreateKeyInput struct {
	AccountID string            `json:"account_id"`
	Name      string            `json:"name"`
	Scopes    []principal.Scope `json:"scopes"`
}

const (
	// KeyPrefix marks exemplar keys so secret scanners and people can spot them.
	KeyPrefix = "exm_"
	// keyDisplayLen is how much of the secret is kept to tell keys apart.
	keyDisplayLen = len(KeyPrefix) + 8
	// touchInterval limits last_used_at writes to one per key per minute.
	touchInterval = time.Minute
)

var (
	nameRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	keyEncoding   = base32.StdEncoding.WithPadding(base32.NoPadding)
	defaultScopes = []principal.Scope{principal.ScopeRead, principal.ScopeWrite}
	adminScopes   = []principal.Scope{principal.ScopeRead, principal.ScopeWrite, principal.ScopeAdmin}
)

// Service manages accounts and keys and authenticates keys.
type Service struct {
	db      xdal.DBInterface
	storage *Storage
	audit   *audit.Service
	now     func() time.Time
}

// NewService returns an account service that records its changes in auditService.
func NewService(db xdal.DBInterface, auditService *audit.Service) *Service {
	return &Service{db: db, storage: NewStorage(db.NewQueryBuilder()), audit: auditService, now: time.Now}
}

// Bootstrap creates the first admin account and its key. It is meant for the
// host (the CLI talks to the database directly) and refuses once any active
// admin exists, so it cannot be used to take over a running instance.
func (s *Service) Bootstrap(ctx context.Context, in CreateAccountInput) (*Account, *IssuedKey, error) {
	if in.Name == "" {
		in.Name = "admin"
	}
	if in.Kind == "" {
		in.Kind = KindDeveloper
	}
	in.Role = principal.RoleAdmin
	if err := validateAccount(in); err != nil {
		return nil, nil, err
	}

	var acc *Account
	var issued *IssuedKey
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		admins, err := s.storage.CountActiveAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if admins > 0 {
			return fmt.Errorf("%w: an admin account already exists; use bootstrap --recover", apperr.ErrConflict)
		}
		acc = s.newAccount(in)
		if err := s.storage.InsertAccount(ctx, tx, acc); err != nil {
			return wrapConflict(err, "account %q already exists", in.Name)
		}
		issued, err = s.issueKey(ctx, tx, acc.ID, "bootstrap", adminScopes)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: acc.ID, Action: audit.ActionBootstrap, TargetType: "account", TargetID: acc.ID,
			Details: map[string]any{"name": acc.Name, "key_id": issued.Key.ID},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return acc, issued, nil
}

// Recover replaces a lost admin key from the host: it revokes every key of the
// named admin account and issues a new admin key.
func (s *Service) Recover(ctx context.Context, name string) (*Account, *IssuedKey, error) {
	var acc *Account
	var issued *IssuedKey
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		var err error
		acc, err = s.storage.GetAccount(ctx, tx, "name = ?", name)
		if err != nil {
			return fmt.Errorf("account %q: %w", name, err)
		}
		if acc.Role != principal.RoleAdmin || acc.DisabledAt != nil {
			return fmt.Errorf("%w: %q is not an active admin account", apperr.ErrInvalid, name)
		}
		revoked, err := s.storage.RevokeKeys(ctx, tx, acc.ID, "", s.now())
		if err != nil {
			return err
		}
		issued, err = s.issueKey(ctx, tx, acc.ID, "recovery", adminScopes)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: acc.ID, Action: audit.ActionRecover, TargetType: "account", TargetID: acc.ID,
			Details: map[string]any{"revoked_keys": revoked, "key_id": issued.Key.ID},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return acc, issued, nil
}

// CreateAccount creates an account. Admin only.
func (s *Service) CreateAccount(ctx context.Context, actor principal.Principal, in CreateAccountInput) (*Account, error) {
	if !actor.IsAdmin() {
		return nil, fmt.Errorf("%w: creating accounts needs an admin key", apperr.ErrForbidden)
	}
	if in.Role == "" {
		in.Role = principal.RoleMember
	}
	if err := validateAccount(in); err != nil {
		return nil, err
	}
	acc := s.newAccount(in)
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		if err := s.storage.InsertAccount(ctx, tx, acc); err != nil {
			return wrapConflict(err, "account %q already exists", in.Name)
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionAccountCreate, TargetType: "account", TargetID: acc.ID,
			Details: map[string]any{"name": acc.Name, "kind": acc.Kind, "role": acc.Role},
		})
	})
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// ListAccounts returns every account to admins and only their own to members.
func (s *Service) ListAccounts(ctx context.Context, actor principal.Principal) ([]Account, error) {
	if !actor.IsAdmin() {
		acc, err := s.GetAccount(ctx, actor, actor.AccountID)
		if err != nil {
			return nil, err
		}
		return []Account{*acc}, nil
	}
	return s.storage.ListAccounts(ctx, s.db)
}

// GetAccount returns an account to itself or to an admin; to anyone else it
// does not exist.
func (s *Service) GetAccount(ctx context.Context, actor principal.Principal, id string) (*Account, error) {
	if id != actor.AccountID && !actor.IsAdmin() {
		return nil, fmt.Errorf("account %s: %w", id, apperr.ErrNotFound)
	}
	return s.storage.GetAccount(ctx, s.db, "id = ?", id)
}

// DisableAccount blocks an account and revokes its keys. The last active admin
// cannot be disabled, so the instance always keeps one.
func (s *Service) DisableAccount(ctx context.Context, actor principal.Principal, id string) error {
	if !actor.IsAdmin() {
		return fmt.Errorf("%w: disabling accounts needs an admin key", apperr.ErrForbidden)
	}
	return s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		acc, err := s.storage.GetAccount(ctx, tx, "id = ?", id)
		if err != nil {
			return fmt.Errorf("account %s: %w", id, err)
		}
		if acc.DisabledAt != nil {
			return nil
		}
		if acc.Role == principal.RoleAdmin {
			admins, err := s.storage.CountActiveAdmins(ctx, tx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return fmt.Errorf("%w: cannot disable the last active admin", apperr.ErrConflict)
			}
		}
		now := s.now()
		if err := s.storage.DisableAccount(ctx, tx, id, now); err != nil {
			return err
		}
		revoked, err := s.storage.RevokeKeys(ctx, tx, id, "", now)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionAccountDisable, TargetType: "account", TargetID: id,
			Details: map[string]any{"revoked_keys": revoked},
		})
	})
}

// CreateKey issues a key. Members may create keys for themselves with at most
// the scopes of the key they are using; admins may create keys for anyone.
// The admin scope is only ever granted to admin accounts, by an admin key.
func (s *Service) CreateKey(ctx context.Context, actor principal.Principal, in CreateKeyInput) (*IssuedKey, error) {
	if in.AccountID == "" {
		in.AccountID = actor.AccountID
	}
	if in.Name == "" {
		return nil, fmt.Errorf("%w: key name is required", apperr.ErrInvalid)
	}
	// Key names follow the account name rules, so what lists, the CLI and
	// the audit log show is never control characters or a wall of text.
	if !nameRe.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: key name must be 1-64 letters, digits, '.', '_' or '-'", apperr.ErrInvalid)
	}
	if len(in.Scopes) == 0 {
		in.Scopes = defaultScopes
	}
	for _, sc := range in.Scopes {
		if !sc.Valid() {
			return nil, fmt.Errorf("%w: unknown scope %q", apperr.ErrInvalid, sc)
		}
	}
	if !actor.IsAdmin() {
		if in.AccountID != actor.AccountID || !actor.CanWrite() {
			return nil, fmt.Errorf("%w: this key cannot create keys for that account", apperr.ErrForbidden)
		}
		for _, sc := range in.Scopes {
			if !actor.Has(sc) {
				return nil, fmt.Errorf("%w: a key cannot grant scope %q it does not have", apperr.ErrForbidden, sc)
			}
		}
	}

	var issued *IssuedKey
	err := s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		acc, err := s.storage.GetAccount(ctx, tx, "id = ?", in.AccountID)
		if err != nil {
			return fmt.Errorf("account %s: %w", in.AccountID, err)
		}
		if acc.DisabledAt != nil {
			return fmt.Errorf("%w: account %s is disabled", apperr.ErrInvalid, acc.Name)
		}
		if slices.Contains(in.Scopes, principal.ScopeAdmin) && acc.Role != principal.RoleAdmin {
			return fmt.Errorf("%w: the admin scope needs an admin account", apperr.ErrInvalid)
		}
		issued, err = s.issueKey(ctx, tx, acc.ID, in.Name, in.Scopes)
		if err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionKeyCreate, TargetType: "api_key", TargetID: issued.Key.ID,
			Details: map[string]any{"account_id": acc.ID, "name": in.Name, "scopes": in.Scopes},
		})
	})
	if err != nil {
		return nil, err
	}
	return issued, nil
}

// ListKeys returns an account's keys (the caller's own when accountID is
// empty). Members may only list their own.
func (s *Service) ListKeys(ctx context.Context, actor principal.Principal, accountID string) ([]APIKey, error) {
	if accountID == "" {
		accountID = actor.AccountID
	}
	if accountID != actor.AccountID && !actor.IsAdmin() {
		return nil, fmt.Errorf("account %s: %w", accountID, apperr.ErrNotFound)
	}
	return s.storage.ListKeys(ctx, s.db, accountID)
}

// RevokeKey revokes one key. Members may revoke their own keys, admins any key.
func (s *Service) RevokeKey(ctx context.Context, actor principal.Principal, keyID string) error {
	return s.db.WithTx(ctx, nil, func(tx *xdal.Tx) error {
		k, err := s.storage.GetKey(ctx, tx, keyID)
		if err != nil || (k.AccountID != actor.AccountID && !actor.IsAdmin()) {
			return fmt.Errorf("key %s: %w", keyID, apperr.ErrNotFound)
		}
		if !actor.IsAdmin() && !actor.CanWrite() {
			return fmt.Errorf("%w: revoking keys needs a write key", apperr.ErrForbidden)
		}
		if k.RevokedAt != nil {
			return nil
		}
		if _, err := s.storage.RevokeKeys(ctx, tx, k.AccountID, k.ID, s.now()); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Event{
			AccountID: actor.AccountID, Action: audit.ActionKeyRevoke, TargetType: "api_key", TargetID: k.ID,
			Details: map[string]any{"account_id": k.AccountID},
		})
	})
}

// Authenticate resolves a secret to the principal it acts as. Unknown, revoked
// and disabled-account keys all return ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, secret string) (principal.Principal, error) {
	if !strings.HasPrefix(secret, KeyPrefix) || len(secret) <= keyDisplayLen {
		return principal.Principal{}, apperr.ErrUnauthenticated
	}
	k, acc, err := s.storage.FindActiveKey(ctx, s.db, hashSecret(secret))
	if err != nil {
		if errors.Is(err, apperr.ErrUnauthenticated) {
			return principal.Principal{}, err
		}
		return principal.Principal{}, fmt.Errorf("looking up key: %w", err)
	}
	now := s.now()
	if k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) >= touchInterval {
		// Best effort: a failed timestamp update must not fail the request.
		_ = s.storage.TouchKey(ctx, s.db, k.ID, now)
	}
	return principal.Principal{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		Role:        acc.Role,
		KeyID:       k.ID,
		Scopes:      k.Scopes,
	}, nil
}

// KeyActive reports whether a key resolved earlier is still active on an
// enabled account: the same conditions Authenticate applies, for a key
// already resolved. Streaming transfers re-check it while they run, so
// revoking a key or disabling an account also stops them.
func (s *Service) KeyActive(ctx context.Context, keyID string) (bool, error) {
	if keyID == "" {
		return false, nil
	}
	return s.storage.KeyActive(ctx, s.db, keyID)
}

func (s *Service) newAccount(in CreateAccountInput) *Account {
	return &Account{
		ID:        storeutil.NewID(),
		Name:      in.Name,
		Kind:      in.Kind,
		Role:      in.Role,
		CreatedAt: s.now(),
	}
}

func (s *Service) issueKey(ctx context.Context, exec xdal.DBExecutor, accountID, name string, scopes []principal.Scope) (*IssuedKey, error) {
	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	k := APIKey{
		ID:        storeutil.NewID(),
		AccountID: accountID,
		Name:      name,
		Prefix:    secret[:keyDisplayLen],
		Scopes:    scopes,
		CreatedAt: s.now(),
	}
	if err := s.storage.InsertKey(ctx, exec, &k, hashSecret(secret)); err != nil {
		return nil, err
	}
	return &IssuedKey{Key: k, Secret: secret}, nil
}

func validateAccount(in CreateAccountInput) error {
	if !nameRe.MatchString(in.Name) {
		return fmt.Errorf("%w: account name must be 1-64 letters, digits, '.', '_' or '-'", apperr.ErrInvalid)
	}
	if !in.Kind.Valid() {
		return fmt.Errorf("%w: kind must be developer, team or app", apperr.ErrInvalid)
	}
	if !in.Role.Valid() {
		return fmt.Errorf("%w: role must be member or admin", apperr.ErrInvalid)
	}
	return nil
}

func wrapConflict(err error, format string, args ...interface{}) error {
	if errors.Is(err, apperr.ErrConflict) {
		return fmt.Errorf("%w: "+format, append([]interface{}{apperr.ErrConflict}, args...)...)
	}
	return err
}

// newSecret returns 256 random bits; their entropy is why a plain SHA-256
// (rather than a slow password hash) is enough to store them.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating key: %w", err)
	}
	return KeyPrefix + strings.ToLower(keyEncoding.EncodeToString(b)), nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
