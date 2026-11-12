package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// SessionCookie is the name of the HttpOnly dashboard session cookie.
const SessionCookie = "arcana_session"

// maxSessionsPerKey caps stored sessions per key; the oldest are dropped first.
const maxSessionsPerKey = 20

const sessionTokenLength = 43 // 32 random bytes, unpadded base64url

// SessionPolicy enables cookie sessions.
type SessionPolicy struct {
	TTL            time.Duration
	AllowedOrigins []string
}

// Session identifies a verified dashboard session.
type Session struct {
	ID        string
	ExpiresAt time.Time
}

type sessionContextKey struct{}

// SessionFromContext returns the session that authenticated the request, if a cookie did.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionContextKey{}).(Session)
	return s, ok
}

// WithSessions enables cookie sessions on a service. Without it cookies are ignored.
func (s *Service) WithSessions(policy SessionPolicy) *Service {
	s.sessions = &policy
	return s
}

// SessionsEnabled reports whether cookie sessions are configured.
func (s *Service) SessionsEnabled() bool { return s.sessions != nil }

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration {
	if s.sessions == nil {
		return 0
	}
	return s.sessions.TTL
}

// CreateSession issues a session for an authenticated key. The token is returned once; only its
// SHA-256 hash is stored.
func (s *Service) CreateSession(ctx context.Context, p Principal) (string, Session, error) {
	if s.sessions == nil {
		return "", Session{}, errors.New("sessions are disabled")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", Session{}, fmt.Errorf("generating session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	id, err := ids.New("ses")
	if err != nil {
		return "", Session{}, err
	}
	now := s.clock.Now()
	expires := now.Add(s.sessions.TTL)
	if _, err = s.store.Queries.DeleteExpiredSessions(ctx, now); err != nil {
		return "", Session{}, fmt.Errorf("pruning sessions: %w", err)
	}
	count, err := s.store.Queries.CountSessionsForKey(ctx, p.ID)
	if err != nil {
		return "", Session{}, fmt.Errorf("counting sessions: %w", err)
	}
	if over := count - (maxSessionsPerKey - 1); over > 0 {
		if _, err = s.store.Queries.DeleteOldestSessionsForKey(ctx, db.DeleteOldestSessionsForKeyParams{KeyID: p.ID, Limit: over}); err != nil {
			return "", Session{}, fmt.Errorf("evicting sessions: %w", err)
		}
	}
	if _, err = s.store.Queries.CreateSession(ctx, db.CreateSessionParams{ID: id, KeyID: p.ID, TokenHash: hash[:], CreatedAt: now, ExpiresAt: expires, LastSeenAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
		return "", Session{}, fmt.Errorf("creating session: %w", err)
	}
	s.audit.Record("session.created", p.ID, "", map[string]any{"session_id": id})
	return token, Session{ID: id, ExpiresAt: expires}, nil
}

// verifySession authenticates a cookie token against current state: expired sessions and sessions
// of revoked keys stop working immediately.
func (s *Service) verifySession(ctx context.Context, token string) (Principal, Session, error) {
	if len(token) != sessionTokenLength {
		return Principal{}, Session{}, InvalidCredentials{}
	}
	hash := sha256.Sum256([]byte(token))
	row, err := s.store.Queries.GetSessionByHash(ctx, hash[:])
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, Session{}, InvalidCredentials{}
	}
	if err != nil {
		return Principal{}, Session{}, fmt.Errorf("verifying session: %w", err)
	}
	now := s.clock.Now()
	if !now.Before(row.ExpiresAt) || row.KeyRevokedAt.Valid {
		return Principal{}, Session{}, InvalidCredentials{}
	}
	if !row.LastSeenAt.Valid || now.Sub(row.LastSeenAt.Time) >= time.Minute {
		if err = s.store.Queries.TouchSession(ctx, db.TouchSessionParams{ID: row.ID, LastSeenAt: sql.NullTime{Time: now, Valid: true}}); err != nil {
			return Principal{}, Session{}, fmt.Errorf("recording session use: %w", err)
		}
	}
	return Principal{ID: row.KeyID, Admin: row.IsAdmin == 1}, Session{ID: row.ID, ExpiresAt: row.ExpiresAt}, nil
}

// EndSession deletes a session; it is idempotent.
func (s *Service) EndSession(ctx context.Context, id string) error {
	if _, err := s.store.Queries.DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("ending session: %w", err)
	}
	return nil
}

// ActiveSessions counts unexpired sessions of unrevoked keys, for metrics.
func (s *Service) ActiveSessions(ctx context.Context) (int64, error) {
	return s.store.Queries.CountActiveSessions(ctx, s.clock.Now())
}
