// Package migrate applies embedded SQL migrations in lexical order, tracking
// applied versions in schema_migrations. Each migration runs in its own
// transaction via the simple query protocol, so multi-statement files with
// plpgsql function bodies ($$ ... $$) apply without statement-splitting.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var files embed.FS

// Run applies every pending migration. It is safe to call on every startup.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return err
	}

	versions, err := pending(applied)
	if err != nil {
		return err
	}

	for _, v := range versions {
		body, err := fs.ReadFile(files, "sql/"+v)
		if err != nil {
			return fmt.Errorf("read %s: %w", v, err)
		}
		if err := apply(ctx, pool, v, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", v, err)
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("load applied versions: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func pending(applied map[string]bool) ([]string, error) {
	entries, err := fs.ReadDir(files, "sql")
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || applied[name] {
			continue
		}
		versions = append(versions, name)
	}
	sort.Strings(versions)
	return versions, nil
}

// apply runs one migration and records its version in the same transaction.
// The simple protocol lets the whole file execute as one batch.
func apply(ctx context.Context, pool *pgxpool.Pool, version, body string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	batch := "BEGIN;\n" + body +
		"\nINSERT INTO schema_migrations (version) VALUES ('" + version + "');\nCOMMIT;"

	mrr := conn.Conn().PgConn().Exec(ctx, batch)
	if _, err := mrr.ReadAll(); err != nil {
		// The failed transaction is already rolled back by the server on error.
		return err
	}
	return nil
}
