package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrSessionNotFound = errors.New("session not found")

type Session struct {
	ID         string
	UserID     string
	CSRFSecret string
	ExpiresAt  time.Time
}

type SessionRepo struct {
	pool *pgxpool.Pool
}

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo { return &SessionRepo{pool: pool} }

// Create issues a session and returns the plaintext token (stored only as a
// hash). The random csrf_secret doubles as the CSRF token for the session.
func (r *SessionRepo) Create(ctx context.Context, userID string, ttl time.Duration) (token string, err error) {
	token, err = randomToken()
	if err != nil {
		return "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, csrf_secret, expires_at)
		VALUES ($1, $2, $3, $4)`,
		userID, hashToken(token), csrf, time.Now().Add(ttl),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// Resolve returns the (unexpired) session and its active user for a token.
func (r *SessionRepo) Resolve(ctx context.Context, token string) (Session, User, error) {
	var s Session
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT s.id::text, s.user_id::text, s.csrf_secret, s.expires_at,
		       u.id::text, u.username, COALESCE(u.email,''), COALESCE(u.password_hash,''),
		       u.role::text, u.auth_source::text, u.is_active
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.is_active`,
		hashToken(token),
	).Scan(&s.ID, &s.UserID, &s.CSRFSecret, &s.ExpiresAt,
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AuthSource, &u.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	return s, u, nil
}

// Delete removes a session (logout).
func (r *SessionRepo) Delete(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}
