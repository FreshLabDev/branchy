// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration struct {
	Version int
	Name    string
	Path    string
}

var migrationFilePattern = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// migrationLockKey is a fixed application-chosen key for the session-level
// advisory lock that serializes migrations across instances. Any constant works
// as long as it is stable across builds.
const migrationLockKey int64 = 0x6272636879 // "brchy"

func RunMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	if dir == "" {
		dir = "migrations"
	}
	migrations, err := ListMigrations(dir)
	if err != nil {
		return err
	}

	// Serialize migrations across instances: two pods starting together must not
	// both run a non-idempotent migration (e.g. 006's DROP/CREATE INDEX). A
	// session-level advisory lock on a dedicated connection blocks the second
	// migrator until the first commits; it then sees every migration already
	// applied and does nothing.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		// Best effort: releasing the connection drops the session lock anyway.
		// Use a background context so unlock still runs if ctx was canceled.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, migration := range migrations {
		applied, err := migrationApplied(ctx, pool, migration.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := os.ReadFile(migration.Path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", migration.Name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.Name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", migration.Name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (version, name)
			VALUES ($1, $2)
			ON CONFLICT (version) DO NOTHING
		`, migration.Version, migration.Name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", migration.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.Name, err)
		}
	}
	return nil
}

func ListMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		matches := migrationFilePattern.FindStringSubmatch(name)
		if matches == nil {
			continue
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", name, err)
		}
		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			Path:    filepath.Join(dir, name),
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

func migrationApplied(ctx context.Context, pool *pgxpool.Pool, version int) (bool, error) {
	var applied bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM schema_migrations WHERE version = $1
		)
	`, version).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("check migration %d: %w", version, err)
	}
	return applied, nil
}
