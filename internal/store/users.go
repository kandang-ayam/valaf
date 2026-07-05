package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUserNotFound = errors.New("user not found")

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Role         string
	AuthSource   string
	IsActive     bool
}

type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

const userCols = `id::text, username, COALESCE(email,''), COALESCE(password_hash,''), role::text, auth_source::text, is_active`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AuthSource, &u.IsActive)
	return u, err
}

// Create inserts a local (or other) account and returns its id.
func (r *UserRepo) Create(ctx context.Context, username, email, passwordHash, role, authSource string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, auth_source)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
		username, nullIfEmpty(email), nullIfEmpty(passwordHash), role, authSource,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

func (r *UserRepo) FindByUsername(ctx context.Context, username string) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username = $1`, username))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return u, err
}

func (r *UserRepo) UpdateLastLogin(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

// UpsertProxyUser resolves (creating on first sight) a user authenticated by a
// trusted reverse proxy. New proxy users default to the read-only viewer role
// until an admin promotes them.
func (r *UserRepo) UpsertProxyUser(ctx context.Context, username string) (User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx, `
		INSERT INTO users (username, auth_source, role)
		VALUES ($1, 'proxy', 'viewer')
		ON CONFLICT (username) DO UPDATE SET last_login_at = now()
		RETURNING `+userCols, username))
	if err != nil {
		return User{}, fmt.Errorf("upsert proxy user: %w", err)
	}
	return u, nil
}
