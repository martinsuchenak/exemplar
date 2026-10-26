package account

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/martinsuchenak/xdal/pkg/xdal"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/principal"
	"github.com/martinsuchenak/exemplar/internal/storeutil"
)

var (
	accountColumns = []string{"id", "name", "kind", "role", "created_at", "disabled_at"}
	keyColumns     = []string{"id", "account_id", "name", "prefix", "scopes", "created_at", "last_used_at", "revoked_at"}
)

// Storage holds the SQL for accounts and api_keys. Only the account service calls it.
type Storage struct {
	qb *xdal.QueryBuilder
}

// NewStorage returns storage that builds queries with qb.
func NewStorage(qb *xdal.QueryBuilder) *Storage {
	return &Storage{qb: qb}
}

// InsertAccount inserts a; a taken name returns apperr.ErrConflict.
func (s *Storage) InsertAccount(ctx context.Context, exec xdal.DBExecutor, a *Account) error {
	query, args, err := s.qb.Insert("accounts").
		Set("id", a.ID).
		Set("name", a.Name).
		Set("kind", string(a.Kind)).
		Set("role", string(a.Role)).
		Set("created_at", storeutil.FormatTime(a.CreatedAt)).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	if storeutil.IsUniqueViolation(err) {
		return apperr.ErrConflict
	}
	return err
}

// GetAccount returns the account matching where (for example "id = ?").
func (s *Storage) GetAccount(ctx context.Context, exec xdal.DBExecutor, where string, arg interface{}) (*Account, error) {
	query, args, err := s.qb.Select(accountColumns...).From("accounts").Where(where, arg).Build()
	if err != nil {
		return nil, err
	}
	a, err := scanAccount(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return a, err
}

// ListAccounts returns every account ordered by name.
func (s *Storage) ListAccounts(ctx context.Context, exec xdal.DBExecutor) ([]Account, error) {
	query, args, err := s.qb.Select(accountColumns...).From("accounts").OrderBy("name").Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accounts := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}

// CountActiveAdmins counts admin accounts that are not disabled.
func (s *Storage) CountActiveAdmins(ctx context.Context, exec xdal.DBExecutor) (int, error) {
	query, args, err := s.qb.Select("COUNT(*)").From("accounts").
		Where("role = ?", string(principal.RoleAdmin)).
		WhereIsNull("disabled_at").
		Build()
	if err != nil {
		return 0, err
	}
	var n int
	err = exec.QueryRow(ctx, query, args...).Scan(&n)
	return n, err
}

// DisableAccount marks an account disabled at at.
func (s *Storage) DisableAccount(ctx context.Context, exec xdal.DBExecutor, id string, at time.Time) error {
	query, args, err := s.qb.Update("accounts").
		Set("disabled_at", storeutil.FormatTime(at)).
		Where("id = ?", id).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// InsertKey stores a key's metadata and the hash of its secret.
func (s *Storage) InsertKey(ctx context.Context, exec xdal.DBExecutor, k *APIKey, hash string) error {
	query, args, err := s.qb.Insert("api_keys").
		Set("id", k.ID).
		Set("account_id", k.AccountID).
		Set("name", k.Name).
		Set("prefix", k.Prefix).
		Set("hash", hash).
		Set("scopes", joinScopes(k.Scopes)).
		Set("created_at", storeutil.FormatTime(k.CreatedAt)).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

// GetKey returns a key's metadata.
func (s *Storage) GetKey(ctx context.Context, exec xdal.DBExecutor, id string) (*APIKey, error) {
	query, args, err := s.qb.Select(keyColumns...).From("api_keys").Where("id = ?", id).Build()
	if err != nil {
		return nil, err
	}
	k, err := scanKey(exec.QueryRow(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return k, err
}

// ListKeys returns an account's keys, oldest first.
func (s *Storage) ListKeys(ctx context.Context, exec xdal.DBExecutor, accountID string) ([]APIKey, error) {
	query, args, err := s.qb.Select(keyColumns...).From("api_keys").
		Where("account_id = ?", accountID).
		OrderBy("created_at").
		Build()
	if err != nil {
		return nil, err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	keys := []APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *k)
	}
	return keys, rows.Err()
}

// RevokeKeys revokes the account's active keys (all of them when keyID is empty)
// and returns how many changed.
func (s *Storage) RevokeKeys(ctx context.Context, exec xdal.DBExecutor, accountID, keyID string, at time.Time) (int64, error) {
	q := s.qb.Update("api_keys").
		Set("revoked_at", storeutil.FormatTime(at)).
		Where("account_id = ?", accountID).
		Where("revoked_at IS NULL")
	if keyID != "" {
		q = q.Where("id = ?", keyID)
	}
	query, args, err := q.Build()
	if err != nil {
		return 0, err
	}
	res, err := exec.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FindActiveKey returns the active key with this hash and its enabled account.
func (s *Storage) FindActiveKey(ctx context.Context, exec xdal.DBExecutor, hash string) (*APIKey, *Account, error) {
	cols := make([]string, 0, len(keyColumns)+len(accountColumns))
	for _, c := range keyColumns {
		cols = append(cols, "k."+c)
	}
	for _, c := range accountColumns {
		cols = append(cols, "a."+c)
	}
	query, args, err := s.qb.Select(cols...).From("api_keys k").
		Join("JOIN accounts a ON a.id = k.account_id").
		Where("k.hash = ?", hash).
		Where("k.revoked_at IS NULL").
		Where("a.disabled_at IS NULL").
		Build()
	if err != nil {
		return nil, nil, err
	}
	var k APIKey
	var a Account
	var scopes, keyCreated, accCreated, kind, role string
	var lastUsed, revoked, disabled sql.NullString
	err = exec.QueryRow(ctx, query, args...).Scan(
		&k.ID, &k.AccountID, &k.Name, &k.Prefix, &scopes, &keyCreated, &lastUsed, &revoked,
		&a.ID, &a.Name, &kind, &role, &accCreated, &disabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, apperr.ErrUnauthenticated
	}
	if err != nil {
		return nil, nil, err
	}
	k.Scopes = splitScopes(scopes)
	k.CreatedAt = storeutil.ParseTime(keyCreated)
	k.LastUsedAt = storeutil.NullTime(lastUsed)
	a.Kind, a.Role = Kind(kind), principal.Role(role)
	a.CreatedAt = storeutil.ParseTime(accCreated)
	return &k, &a, nil
}

// TouchKey sets a key's last_used_at.
func (s *Storage) TouchKey(ctx context.Context, exec xdal.DBExecutor, id string, at time.Time) error {
	query, args, err := s.qb.Update("api_keys").
		Set("last_used_at", storeutil.FormatTime(at)).
		Where("id = ?", id).
		Build()
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, query, args...)
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanAccount(row scanner) (*Account, error) {
	var a Account
	var kind, role, created string
	var disabled sql.NullString
	if err := row.Scan(&a.ID, &a.Name, &kind, &role, &created, &disabled); err != nil {
		return nil, err
	}
	a.Kind, a.Role = Kind(kind), principal.Role(role)
	a.CreatedAt = storeutil.ParseTime(created)
	a.DisabledAt = storeutil.NullTime(disabled)
	return &a, nil
}

func scanKey(row scanner) (*APIKey, error) {
	var k APIKey
	var scopes, created string
	var lastUsed, revoked sql.NullString
	if err := row.Scan(&k.ID, &k.AccountID, &k.Name, &k.Prefix, &scopes, &created, &lastUsed, &revoked); err != nil {
		return nil, err
	}
	k.Scopes = splitScopes(scopes)
	k.CreatedAt = storeutil.ParseTime(created)
	k.LastUsedAt = storeutil.NullTime(lastUsed)
	k.RevokedAt = storeutil.NullTime(revoked)
	return &k, nil
}

func joinScopes(scopes []principal.Scope) string {
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = string(s)
	}
	return strings.Join(parts, ",")
}

func splitScopes(s string) []principal.Scope {
	if s == "" {
		return []principal.Scope{}
	}
	parts := strings.Split(s, ",")
	scopes := make([]principal.Scope, len(parts))
	for i, p := range parts {
		scopes[i] = principal.Scope(p)
	}
	return scopes
}
