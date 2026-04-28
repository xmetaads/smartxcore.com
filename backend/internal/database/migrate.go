package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// Migrate applies any pending .up.sql files from the given directory.
// Tracks state in a `schema_migrations` table; each file's SHA256 is
// recorded so a modified migration is treated as an error rather than
// silently re-run. Designed to be safe to run on every server startup.
func Migrate(ctx context.Context, db *DB, migrationsDir string) error {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return err
	}

	files, err := discoverMigrations(migrationsDir)
	if err != nil {
		return err
	}

	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, f := range files {
		if existing, ok := applied[f.version]; ok {
			if existing != f.checksum {
				return fmt.Errorf("migration %s checksum mismatch (applied=%s file=%s) — refusing to re-run", f.name, existing, f.checksum)
			}
			continue
		}

		log.Info().Str("migration", f.name).Msg("applying migration")
		if err := applyMigration(ctx, db, f); err != nil {
			return fmt.Errorf("apply %s: %w", f.name, err)
		}
		log.Info().Str("migration", f.name).Msg("migration applied")
	}
	return nil
}

type migrationFile struct {
	version  string
	name     string
	path     string
	sql      string
	checksum string
}

func discoverMigrations(dir string) ([]migrationFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	out := make([]migrationFile, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		version := strings.TrimSuffix(name, ".up.sql")
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		hash := sha256.Sum256(data)
		out = append(out, migrationFile{
			version:  version,
			name:     name,
			path:     path,
			sql:      string(data),
			checksum: hex.EncodeToString(hash[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func ensureMigrationsTable(ctx context.Context, db *DB) error {
	_, err := db.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			checksum   TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func loadAppliedMigrations(ctx context.Context, db *DB) (map[string]string, error) {
	rows, err := db.Pool.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var v, cs string
		if err := rows.Scan(&v, &cs); err != nil {
			return nil, err
		}
		out[v] = cs
	}
	return out, rows.Err()
}

func applyMigration(ctx context.Context, db *DB, f migrationFile) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, f.sql); err != nil {
		// Most migrations should be tx-safe. If a future migration needs
		// CREATE INDEX CONCURRENTLY, run it as a separate non-tx file.
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)
	`, f.version, f.checksum)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
