package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed migrations_pg/*.sql
var pgMigrationsFS embed.FS

// Migrations returns the embedded goose migration files for SQLite.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // embedded path is static
	}
	return sub
}

// MigrationsPG returns the embedded goose migration files for PostgreSQL.
func MigrationsPG() fs.FS {
	sub, err := fs.Sub(pgMigrationsFS, "migrations_pg")
	if err != nil {
		panic(err) // embedded path is static
	}
	return sub
}

// MigrationResult describes one applied migration.
type MigrationResult struct {
	Version int64
	Source  string
}

func (d *DB) provider() (*goose.Provider, error) {
	if d.Dialect() == DialectPostgres {
		return goose.NewProvider(goose.DialectPostgres, d.DB, MigrationsPG())
	}
	return goose.NewProvider(goose.DialectSQLite3, d.DB, Migrations())
}

// Migrate applies all pending migrations and returns those applied.
func (d *DB) Migrate(ctx context.Context) ([]MigrationResult, error) {
	p, err := d.provider()
	if err != nil {
		return nil, fmt.Errorf("db: migrations: %w", err)
	}
	res, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: migrate up: %w", err)
	}
	out := make([]MigrationResult, 0, len(res))
	for _, r := range res {
		out = append(out, MigrationResult{Version: r.Source.Version, Source: r.Source.Path})
	}
	return out, nil
}

// SchemaVersion returns the current goose schema version (0 when unmigrated).
func (d *DB) SchemaVersion(ctx context.Context) (int64, error) {
	p, err := d.provider()
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// LatestSchemaVersion returns the newest embedded migration version.
func (d *DB) LatestSchemaVersion() (int64, error) {
	p, err := d.provider()
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, s := range p.ListSources() {
		latest = max(latest, s.Version)
	}
	return latest, nil
}
