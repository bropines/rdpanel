// Package dbtest opens panel databases for tests: a SQLite file in the test's
// temporary directory by default, or a fresh PostgreSQL database when
// RDPANEL_TEST_PG_DSN is set, so the same suites run on both engines (CI runs
// the PostgreSQL pass against a postgres service container).
//
// On PostgreSQL every test gets its own database cloned from a migrated
// template (CREATE DATABASE ... TEMPLATE), dropped when the test ends. The
// template is named after a digest of the migrations and reused across runs.
// The DSN's role needs CREATEDB.
package dbtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// EnvPostgresDSN names the variable holding a PostgreSQL DSN (URL or
// keyword/value form) to run database tests against.
const EnvPostgresDSN = "RDPANEL_TEST_PG_DSN"

// PostgresDSN returns the PostgreSQL DSN tests run against ("" = SQLite).
func PostgresDSN() string { return strings.TrimSpace(os.Getenv(EnvPostgresDSN)) }

// Postgres reports whether tests run against PostgreSQL.
func Postgres() bool { return PostgresDSN() != "" }

// Open returns a migrated database that is closed (and on PostgreSQL dropped)
// when the test ends.
func Open(t testing.TB) *db.DB {
	t.Helper()
	if dsn := PostgresDSN(); dsn != "" {
		return openPostgres(t, dsn, true)
	}
	d := OpenSQLite(t, filepath.Join(t.TempDir(), "panel.db"))
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

// OpenEmpty returns an unmigrated database (tests stepping through
// migrations). It is closed (and on PostgreSQL dropped) when the test ends.
func OpenEmpty(t testing.TB) *db.DB {
	t.Helper()
	if dsn := PostgresDSN(); dsn != "" {
		return openPostgres(t, dsn, false)
	}
	return OpenSQLite(t, filepath.Join(t.TempDir(), "panel.db"))
}

// OpenSQLite opens (without migrating) the SQLite database at path, closed when
// the test ends. For tests that are about SQLite specifics.
func OpenSQLite(t testing.TB, path string) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), path, db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// PostgresURL creates a fresh, migrated PostgreSQL database for the test
// (dropped when it ends) and returns its DSN, for tests that open the
// database themselves (through the config, for instance). Only call it when
// Postgres reports true.
func PostgresURL(t testing.TB) string {
	t.Helper()
	dsn := PostgresDSN()
	if dsn == "" {
		t.Fatal("dbtest: PostgresURL without " + EnvPostgresDSN)
	}
	return withDatabase(dsn, createDatabase(t, dsn, true))
}

func openPostgres(t testing.TB, dsn string, migrated bool) *db.DB {
	t.Helper()
	name := createDatabase(t, dsn, migrated)
	// Closed before the database is dropped (cleanups run last-in first-out).
	d, err := db.OpenPostgres(context.Background(), withDatabase(dsn, name), db.PostgresOptions{MaxOpenConns: 8, MaxIdleConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// createDatabase creates a database (a copy of the migrated template, or
// empty) that is dropped when the test ends, and returns its name.
func createDatabase(t testing.TB, dsn string, migrated bool) string {
	t.Helper()
	ctx := context.Background()
	admin, err := adminPool(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "rdpanel_test_" + randomHex(8)
	stmt := "CREATE DATABASE " + name
	if migrated {
		tmpl, err := template(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		stmt += " TEMPLATE " + tmpl
	}
	if _, err := admin.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("dbtest: %s: %v", stmt, err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("dbtest: drop %s: %v", name, err)
		}
	})
	return name
}

var (
	adminMu sync.Mutex
	admins  = map[string]*sql.DB{}

	tmplMu sync.Mutex
	tmpls  = map[string]string{}
)

// adminPool returns a process-wide pool on the DSN's own database, used to
// create and drop the per-test databases.
func adminPool(dsn string) (*sql.DB, error) {
	adminMu.Lock()
	defer adminMu.Unlock()
	if p, ok := admins[dsn]; ok {
		return p, nil
	}
	d, err := db.OpenPostgres(context.Background(), dsn, db.PostgresOptions{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		return nil, fmt.Errorf("dbtest: connect %s: %w", EnvPostgresDSN, err)
	}
	admins[dsn] = d.DB
	return d.DB, nil
}

// template returns the name of a database holding the fully migrated schema,
// creating it on first use. Its name carries a digest of the migrations, so a
// schema change gets a new template; concurrent test binaries serialize on an
// advisory lock.
func template(ctx context.Context, dsn string) (string, error) {
	tmplMu.Lock()
	defer tmplMu.Unlock()
	if name, ok := tmpls[dsn]; ok {
		return name, nil
	}
	digest, err := migrationsDigest()
	if err != nil {
		return "", err
	}
	name := "rdpanel_tmpl_" + digest
	admin, err := adminPool(dsn)
	if err != nil {
		return "", err
	}
	conn, err := admin.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('rdpanel-dbtest-template'))`); err != nil {
		return "", err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext('rdpanel-dbtest-template'))`)
	}()
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		if err := buildTemplate(ctx, conn, dsn, name); err != nil {
			return "", err
		}
	}
	tmpls[dsn] = name
	return name, nil
}

func buildTemplate(ctx context.Context, conn *sql.Conn, dsn, name string) error {
	building := name + "_build"
	if _, err := conn.ExecContext(ctx, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CREATE DATABASE "+building); err != nil {
		return fmt.Errorf("dbtest: create template: %w", err)
	}
	d, err := db.OpenPostgres(ctx, withDatabase(dsn, building), db.PostgresOptions{MaxOpenConns: 1})
	if err != nil {
		return err
	}
	_, err = d.Migrate(ctx)
	_ = d.Close()
	if err != nil {
		return fmt.Errorf("dbtest: migrate template: %w", err)
	}
	// Connections of the closed pool may linger for a moment; a template
	// must have none when it is copied.
	for i := 0; ; i++ {
		_, err = conn.ExecContext(ctx, "ALTER DATABASE "+building+" RENAME TO "+name)
		if err == nil || i == 50 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func migrationsDigest() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(db.MigrationsPG(), ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := fs.ReadFile(db.MigrationsPG(), path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16], err
}

// withDatabase returns dsn pointing at database name.
func withDatabase(dsn, name string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err == nil {
			u.Path = "/" + name
			return u.String()
		}
	}
	return dsn + " dbname=" + name
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
