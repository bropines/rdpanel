package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLegacyFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "research", "legacy-rustdeskapi-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var ddl strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "CREATE ") && !strings.HasPrefix(line, "CREATE TABLE sqlite_sequence") {
			ddl.WriteString(line + "\n")
		}
	}
	path := filepath.Join(t.TempDir(), "rustdeskapi.db")
	d, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{ddl.String(),
		`INSERT INTO versions (version) VALUES (265)`,
		`INSERT INTO groups (id, name, type) VALUES (1, 'Default group', 1)`,
		`INSERT INTO users (id, username, password, group_id, is_admin, status) VALUES (1, 'admin', '5f4dcc3b5aa765d61d8327deb882cf99', 1, 1, 1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCLIMigrateLegacy(t *testing.T) {
	env, dir := testEnv(t, nil)
	src := newLegacyFile(t)
	report := filepath.Join(dir, "report.json")
	ctx := context.Background()
	runCLI := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(ctx, args, env, Stdio{In: strings.NewReader(""), Out: &out, Err: &errb})
		return code, out.String(), errb.String()
	}

	if code, _, _ := runCLI("migrate-legacy"); code != 2 {
		t.Fatalf("missing --from: exit %d", code)
	}
	code, out, errOut := runCLI("migrate-legacy", "--from", src, "--dry-run", "--report", report)
	if code != 0 || !strings.Contains(out, "dry_run") || !strings.Contains(out, "dry run: nothing was written") {
		t.Fatalf("dry run: %d\n%s\n%s", code, out, errOut)
	}
	var rep struct {
		Status string `json:"status"`
		Tables map[string]struct {
			Imported int `json:"imported"`
		} `json:"tables"`
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rep); err != nil || rep.Status != "dry_run" || rep.Tables["users"].Imported != 1 {
		t.Fatalf("report: %v %s", err, raw)
	}

	if code, out, errOut = runCLI("migrate-legacy", "--from", src); code != 0 || !strings.Contains(out, "succeeded") {
		t.Fatalf("import: %d\n%s\n%s", code, out, errOut)
	}
	if code, _, errOut = runCLI("migrate-legacy", "--from", src); code != 1 || !strings.Contains(errOut, "not empty") {
		t.Fatalf("second import: %d %s", code, errOut)
	}
}

// TestCLIMigrateLegacyRefusesPostgres: the importer is SQLite-only and must
// fail before touching a PostgreSQL target (the DSN is never dialled here).
func TestCLIMigrateLegacyRefusesPostgres(t *testing.T) {
	env, _ := testEnv(t, map[string]string{"RDPANEL_DATABASE_URL": "postgres://u:p@127.0.0.1:1/x"})
	var out, errb bytes.Buffer
	code := Main(context.Background(), []string{"migrate-legacy", "--from", newLegacyFile(t)}, env,
		Stdio{In: strings.NewReader(""), Out: &out, Err: &errb})
	if code != 1 || !strings.Contains(errb.String(), "PostgreSQL is not supported") {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
}
