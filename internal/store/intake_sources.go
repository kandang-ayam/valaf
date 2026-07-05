package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSourceNotFound is returned when no active intake source matches a name.
var ErrSourceNotFound = errors.New("intake source not found")

// Source is an intake webhook credential record.
type Source struct {
	ID         string
	Name       string
	SourceType string
	AuthMethod string
	tokenHash  string // sha-256 hex of the shared token; "" if unset
	hmacSecret string // "" if unset
}

type SourceRepo struct {
	pool *pgxpool.Pool
}

func NewSourceRepo(pool *pgxpool.Pool) *SourceRepo { return &SourceRepo{pool: pool} }

// FindActive looks up an active source by name.
func (r *SourceRepo) FindActive(ctx context.Context, name string) (Source, error) {
	var s Source
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, source_type, auth_method,
		       COALESCE(token_hash, ''), COALESCE(hmac_secret, '')
		FROM intake_sources
		WHERE name = $1 AND is_active`,
		name,
	).Scan(&s.ID, &s.Name, &s.SourceType, &s.AuthMethod, &s.tokenHash, &s.hmacSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrSourceNotFound
	}
	if err != nil {
		return Source{}, fmt.Errorf("find intake source: %w", err)
	}
	return s, nil
}

// TouchLastSeen records that a source just delivered a webhook (best-effort).
func (r *SourceRepo) TouchLastSeen(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE intake_sources SET last_seen_at = now() WHERE id = $1`, id)
	return err
}

// UpsertSharedToken creates or rotates a shared-token source and returns the
// freshly generated plaintext token (shown once; only its hash is stored).
func (r *SourceRepo) UpsertSharedToken(ctx context.Context, name, sourceType string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := hashToken(token)
	_, err = r.pool.Exec(ctx, `
		INSERT INTO intake_sources (name, source_type, auth_method, token_hash, is_active)
		VALUES ($1, $2, 'shared_token', $3, true)
		ON CONFLICT (name) DO UPDATE SET
			source_type = EXCLUDED.source_type,
			auth_method = 'shared_token',
			token_hash  = EXCLUDED.token_hash,
			hmac_secret = NULL,
			is_active   = true`,
		name, sourceType, hash,
	)
	if err != nil {
		return "", fmt.Errorf("upsert intake source: %w", err)
	}
	return token, nil
}

// VerifySharedToken constant-time compares a presented token against the stored hash.
func (s Source) VerifySharedToken(presented string) bool {
	if s.tokenHash == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(presented)), []byte(s.tokenHash)) == 1
}

// VerifyHMAC checks an HMAC-SHA256 signature (hex) of the raw body.
func (s Source) VerifyHMAC(body []byte, signatureHex string) bool {
	if s.hmacSecret == "" || signatureHex == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.hmacSecret))
	mac.Write(body)
	expected := mac.Sum(nil)
	got, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
