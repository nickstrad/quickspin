package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

const currentMigrationVersion uint = 1

func TestNewMigratesFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st := newMigrationTestStore(t, path)

	assertMigrationVersion(t, st.db, currentMigrationVersion)
	assertSchemaObjects(t, st.db, map[string]string{
		"sandboxes":         "table",
		"idempotency_keys":  "table",
		"events":            "table",
		"events_sandbox_id": "index",
		"schema_migrations": "table",
	})
}

func TestNewReopensMigratedDatabaseWithoutLosingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	first := openMigrationTestStore(t, path)

	image := "alpine:3.20"
	expiresAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	created, err := first.CreateSandbox(t.Context(), "reopen-key", sandbox.SpecFile{Image: &image}, expiresAt)
	if err != nil {
		cleanupMigrationTestStore(t, first)
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	if err := first.Cleanup(); err != nil {
		t.Fatalf("first Cleanup() error = %v, want nil", err)
	}

	reopened := newMigrationTestStore(t, path)

	got, err := reopened.GetSandbox(t.Context(), created.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox(%q) after reopen error = %v, want nil", created.SandboxID, err)
	}
	if got.SandboxID != created.SandboxID || got.State != created.State || !got.ExpiresAt.Equal(expiresAt) {
		t.Errorf("GetSandbox() after reopen = %#v, want sandbox ID %q, state %q, expiry %v", got, created.SandboxID, created.State, expiresAt)
	}

	key, err := reopened.GetIdempotencyKey(t.Context(), "reopen-key")
	if err != nil {
		t.Fatalf("GetIdempotencyKey(reopen-key) after reopen error = %v, want nil", err)
	}
	if key == nil || key.SandboxID != created.SandboxID {
		t.Errorf("GetIdempotencyKey(reopen-key) after reopen = %#v, want sandbox ID %q", key, created.SandboxID)
	}

	assertMigrationVersion(t, reopened.db, currentMigrationVersion)
}

func TestEmbeddedMigrationDownUpRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round-trip.db")
	st := newMigrationTestStore(t, path)
	migrator := newMigrationTestMigrator(t, st.db, path)

	if err := migrator.Down(); err != nil {
		t.Fatalf("Down() error = %v, want nil", err)
	}
	if got := domainSchemaObjectCount(t, st.db); got != 0 {
		t.Errorf("domain schema object count after Down() = %d, want 0", got)
	}

	if err := migrator.Up(); err != nil {
		t.Fatalf("Up() error = %v, want nil", err)
	}
	if got := domainSchemaObjectCount(t, st.db); got != 4 {
		t.Errorf("domain schema object count after Up() = %d, want 4", got)
	}
	assertMigrationVersion(t, st.db, currentMigrationVersion)
}

func TestNewFailsWhenMigrationConflictsWithExistingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict.db")
	raw, err := sql.Open(DefaultDriverType, DSN(path))
	if err != nil {
		t.Fatalf("sql.Open(%q) error = %v, want nil", path, err)
	}
	if _, err := raw.ExecContext(t.Context(), `CREATE TABLE sandboxes (wrong TEXT)`); err != nil {
		raw.Close()
		t.Fatalf("creating conflicting schema error = %v, want nil", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing conflicting database error = %v, want nil", err)
	}

	st, err := New(t.Context(), path, "", migrationTestLogger())
	registerMigrationTestStoreCleanup(t, st)
	if err == nil {
		t.Fatal("New() error = nil, want migration conflict")
	}
	if st != nil {
		t.Errorf("New() store = %#v, want nil after migration failure", st)
	}
}

func TestMigrationStartupHonorsCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canceled.db")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	st, err := New(ctx, path, "", migrationTestLogger())
	registerMigrationTestStoreCleanup(t, st)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("New() error = %v, want context.Canceled", err)
	}
	if st != nil {
		t.Errorf("New() store = %#v, want nil for canceled startup", st)
	}

	raw, err := sql.Open(DefaultDriverType, DSN(path))
	if err != nil {
		t.Fatalf("sql.Open(%q) after canceled startup error = %v, want nil", path, err)
	}
	defer raw.Close()

	var domainTableCount int
	if err := raw.QueryRowContext(t.Context(), `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name IN ('sandboxes', 'idempotency_keys', 'events')`).Scan(&domainTableCount); err != nil {
		t.Fatalf("querying schema after canceled startup error = %v, want nil", err)
	}
	if domainTableCount != 0 {
		t.Errorf("domain table count after canceled startup = %d, want 0", domainTableCount)
	}
}

func newMigrationTestStore(t *testing.T, path string) *Store {
	t.Helper()

	st := openMigrationTestStore(t, path)
	registerMigrationTestStoreCleanup(t, st)
	return st
}

func openMigrationTestStore(t *testing.T, path string) *Store {
	t.Helper()

	st, err := New(t.Context(), path, "", migrationTestLogger())
	if err != nil {
		t.Fatalf("New(%q) error = %v, want nil", path, err)
	}
	return st
}

func registerMigrationTestStoreCleanup(t *testing.T, st *Store) {
	t.Helper()

	if st != nil {
		t.Cleanup(func() {
			cleanupMigrationTestStore(t, st)
		})
	}
}

func cleanupMigrationTestStore(t *testing.T, st *Store) {
	t.Helper()

	if err := st.Cleanup(); err != nil {
		t.Errorf("Cleanup() error = %v, want nil", err)
	}
}

func assertMigrationVersion(t *testing.T, db *sql.DB, want uint) {
	t.Helper()

	var version uint
	var dirty bool
	if err := db.QueryRowContext(t.Context(), `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("reading migration version error = %v, want nil", err)
	}
	if version != want || dirty {
		t.Errorf("migration version = (%d, %t), want (%d, false)", version, dirty, want)
	}
}

func assertSchemaObjects(t *testing.T, db *sql.DB, want map[string]string) {
	t.Helper()

	for name, objectType := range want {
		var got string
		err := db.QueryRowContext(t.Context(), `SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&got)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			t.Errorf("migrated schema is missing %q", name)
		case err != nil:
			t.Fatalf("querying sqlite object %q error = %v, want nil", name, err)
		case got != objectType:
			t.Errorf("sqlite object %q type = %q, want %q", name, got, objectType)
		}
	}
}

func newMigrationTestMigrator(t *testing.T, db *sql.DB, path string) *migrate.Migrate {
	t.Helper()

	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("iofs.New() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := sourceDriver.Close(); err != nil {
			t.Errorf("closing migration source error = %v, want nil", err)
		}
	})

	databaseDriver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{DatabaseName: path})
	if err != nil {
		t.Fatalf("sqlite.WithInstance() error = %v, want nil", err)
	}
	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", databaseDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance() error = %v, want nil", err)
	}
	return migrator
}

func domainSchemaObjectCount(t *testing.T, db *sql.DB) int {
	t.Helper()

	var count int
	if err := db.QueryRowContext(t.Context(), `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE name IN ('sandboxes', 'idempotency_keys', 'events', 'events_sandbox_id')`).Scan(&count); err != nil {
		t.Fatalf("counting domain schema objects error = %v, want nil", err)
	}
	return count
}

func migrationTestLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
