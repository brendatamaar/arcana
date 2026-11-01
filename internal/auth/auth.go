// Package auth manages API keys and request principals.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// Clock supplies time for key creation and last-used debounce.
type Clock interface{ Now() time.Time }

// SystemClock reads wall time in the production shell.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Principal is the authenticated identity, without key material.
type Principal struct {
	ID    string
	Admin bool
}

// Key is safe display metadata; raw values and hashes are excluded.
type Key struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Admin     bool       `json:"is_admin"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Service owns local key operations.
type Service struct {
	store *store.Store
	clock Clock
}

// New constructs a service with an explicit clock.
func New(s *store.Store, clock Clock) *Service { return &Service{store: s, clock: clock} }

// InvalidCredentials is returned for malformed, unknown and revoked keys.
type InvalidCredentials struct{}

func (InvalidCredentials) Error() string { return "invalid credentials" }

// Generate creates an unbiased base62 credential and its SHA-256 hash.
func Generate() (string, [32]byte, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var value strings.Builder
	value.WriteString("arc_live_")
	// Rejection sampling avoids modulo bias; bound work even on entropy failure.
	for attempts := 0; value.Len() < 41 && attempts < 1024; attempts++ {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", [32]byte{}, fmt.Errorf("generating credential: %w", err)
		}
		for _, n := range b {
			if n < 248 && value.Len() < 41 {
				value.WriteByte(alphabet[int(n)%62])
			}
		}
	}
	if value.Len() != 41 {
		return "", [32]byte{}, fmt.Errorf("generating credential: entropy rejection limit")
	}
	raw := value.String()
	return raw, sha256.Sum256([]byte(raw)), nil
}

func validCredential(raw string) bool {
	if len(raw) != 41 || !strings.HasPrefix(raw, "arc_live_") {
		return false
	}
	for _, c := range raw[9:] {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func makeKey(name string, admin bool, now time.Time) (db.CreateKeyParams, string, error) {
	if len(name) == 0 || len(name) > 128 || strings.ContainsRune(name, 0) {
		return db.CreateKeyParams{}, "", fmt.Errorf("key name must contain 1..128 bytes without NUL")
	}
	raw, hash, err := Generate()
	if err != nil {
		return db.CreateKeyParams{}, "", err
	}
	id, err := ids.New("key")
	if err != nil {
		return db.CreateKeyParams{}, "", err
	}
	var flag int64
	if admin {
		flag = 1
	}
	return db.CreateKeyParams{ID: id, Name: name, Prefix: raw[9:17], Hash: hash[:], IsAdmin: flag, CreatedAt: now}, raw, nil
}

// Create stores a key's hash and returns the secret once to the caller.
func (s *Service) Create(ctx context.Context, name string, admin bool) (Key, string, error) {
	params, raw, err := makeKey(name, admin, s.clock.Now())
	if err != nil {
		return Key{}, "", err
	}
	row, err := s.store.Queries.CreateKey(ctx, params)
	if err != nil {
		return Key{}, "", fmt.Errorf("creating key: %w", err)
	}
	return display(row), raw, nil
}

// Verify authenticates a key against current database state, never a revocation cache.
func (s *Service) Verify(ctx context.Context, raw string) (Principal, error) {
	if !validCredential(raw) {
		return Principal{}, InvalidCredentials{}
	}
	hash := sha256.Sum256([]byte(raw))
	row, err := s.store.Queries.GetKeyByHash(ctx, hash[:])
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, InvalidCredentials{}
	}
	if err != nil {
		return Principal{}, fmt.Errorf("verifying key: %w", err)
	}
	if subtle.ConstantTimeCompare(row.Hash, hash[:]) != 1 || row.RevokedAt.Valid {
		return Principal{}, InvalidCredentials{}
	}
	now := s.clock.Now()
	if !row.LastUsedAt.Valid || now.Sub(row.LastUsedAt.Time) >= time.Minute {
		if err = s.store.Queries.TouchKey(ctx, db.TouchKeyParams{ID: row.ID, LastUsedAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
			return Principal{}, fmt.Errorf("recording key use: %w", err)
		}
	}
	return Principal{ID: row.ID, Admin: row.IsAdmin == 1}, nil
}

// List returns a bounded page of display metadata.
func (s *Service) List(ctx context.Context, after string, limit int64) ([]Key, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("key list limit must be 1..1000")
	}
	rows, err := s.store.Queries.ListKeys(ctx, db.ListKeysParams{ID: after, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}
	keys := make([]Key, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, display(row))
	}
	return keys, nil
}

// Revoke is idempotent and takes effect on subsequent authentication requests.
func (s *Service) Revoke(ctx context.Context, id string) error {
	if _, err := s.store.Queries.RevokeKey(ctx, db.RevokeKeyParams{ID: id, RevokedAt: sql.NullTime{Time: s.clock.Now(), Valid: true}}); err != nil {
		return fmt.Errorf("revoking key: %w", err)
	}
	return nil
}

func display(row db.ApiKey) Key {
	result := Key{ID: row.ID, Name: row.Name, Prefix: row.Prefix, Admin: row.IsAdmin == 1, CreatedAt: row.CreatedAt}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		result.RevokedAt = &t
	}
	return result
}
