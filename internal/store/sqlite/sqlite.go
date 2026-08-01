package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	_ "modernc.org/sqlite"
)

const (
	DefaultDBPath     = "control-plane.db"
	DefaultDriverType = "sqlite"
)

type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

var _ store.Store = (*Store)(nil)

// platform_id is the persisted name of Sandbox.SandboxID.
const SandboxSchema = `CREATE TABLE IF NOT EXISTS sandboxes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform_id TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL,
		spec TEXT NOT NULL CHECK(json_valid(spec)),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

// Idempotency keys reference public sandbox IDs rather than internal row IDs.
const IdempotencyKeySchema = `
CREATE TABLE IF NOT EXISTS idempotency_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sandbox_id TEXT NOT NULL,
		idempotency_key TEXT NOT NULL UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(sandbox_id) REFERENCES sandboxes(platform_id) ON DELETE CASCADE
);`

const InsertIdempotencyKeyQuery = `INSERT INTO idempotency_keys
(idempotency_key, sandbox_id)
 VALUES (?, ?)
 RETURNING id, sandbox_id, idempotency_key, created_at, updated_at;`

const GetIdempotencyKeyQuery = `SELECT
	id, sandbox_id, idempotency_key, created_at, updated_at
FROM
	idempotency_keys
WHERE idempotency_key = ?;`

// The `state = ?` predicate is the transition gate: matching the expected
// current state and writing the new one in a single statement is atomic, so
// two writers cannot both observe `running` and both move out of it.
const UpdateSandboxStateQuery = `
UPDATE sandboxes
SET state = ?, updated_at = CURRENT_TIMESTAMP
WHERE platform_id = ? AND state = ?
RETURNING id, platform_id, state, spec, created_at, updated_at;`

const InsertSandboxQuery = `
INSERT INTO
	sandboxes (state, spec, platform_id)
VALUES (?, ?, ?)
RETURNING id, platform_id, state, spec, created_at, updated_at;`

const GetSandboxQuery = `
SELECT id, platform_id, state, spec, created_at, updated_at
FROM sandboxes
WHERE platform_id = ?;`

const GetSandboxesQuery = `
SELECT id, platform_id, state, spec, created_at, updated_at
FROM sandboxes;`

func New(ctx context.Context, dbFilePath string, dbDriverType string, logger *slog.Logger) (*Store, error) {

	if dbFilePath == "" {
		dbFilePath = DefaultDBPath
	}

	if logger == nil {
		return nil, store.E("sqlite.New", "logger is required", nil)
	}

	if dbDriverType == "" {
		dbDriverType = DefaultDriverType
	}

	// foreign_keys is off by default in SQLite, so without it the FK on
	// idempotency_keys.sandbox_id is documentation rather than a constraint.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", dbFilePath)
	db, err := sql.Open(dbDriverType, dsn)
	if err != nil {
		return nil, store.E("sqlite.New",
			fmt.Sprintf("opening %s database %s", dbDriverType, dbFilePath), err)
	}

	db.SetMaxOpenConns(1)    // Only allow 1 write connection at a time to prevent locks
	db.SetMaxIdleConns(1)    // Keep that connection open
	db.SetConnMaxLifetime(0) // Reuse connections indefinitely

	schemas := []string{SandboxSchema, IdempotencyKeySchema}
	for _, schema := range schemas {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			_ = db.Close()
			return nil, store.E("sqlite.New", "executing schema", err)
		}
	}

	logger.InfoContext(ctx, "store opened", "path", dbFilePath, "driver", dbDriverType)

	return &Store{
		db:     db,
		logger: logger,
	}, nil
}

func (s *Store) GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*sandbox.IdempotencyKey, error) {

	k, err := scanIdempotencyKey(s.db.QueryRowContext(ctx, GetIdempotencyKeyQuery, idempotencyKey))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.logger.DebugContext(ctx, "idempotency key miss", "idempotencyKey", idempotencyKey)
			return nil, nil
		}
		return nil, store.Wrap("sqlite.Store.GetIdempotencyKey",
			fmt.Sprintf("reading idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "idempotency key hit", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

func scanIdempotencyKey(row *sql.Row) (*sandbox.IdempotencyKey, error) {
	k := &sandbox.IdempotencyKey{}

	err := row.Scan(
		&k.ID,
		&k.SandboxID,
		&k.Key,
		&k.CreatedAt,
		&k.UpdatedAt,
	)
	if err != nil {
		// A bare sentinel, not E: a missing row is an expected outcome (every
		// fresh create misses the key lookup), so no stack capture.
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, store.E("sqlite.scanIdempotencyKey", "scanning idempotency key row", err)
	}

	return k, nil
}

func (s *Store) CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*sandbox.IdempotencyKey, error) {
	k, err := scanIdempotencyKey(s.db.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sandboxID))
	if err != nil {
		return nil, store.Wrap("sqlite.Store.CreateIdempotencyKey",
			fmt.Sprintf("recording idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "recorded idempotency key", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSandbox(scanner rowScanner) (*sandbox.Sandbox, error) {
	sbx := &sandbox.Sandbox{}
	var rawSpec string

	err := scanner.Scan(
		&sbx.ID,
		&sbx.SandboxID,
		&sbx.State,
		&rawSpec,
		&sbx.CreatedAt,
		&sbx.UpdatedAt,
	)
	if err != nil {
		return nil, store.E("sqlite.scanSandbox", "scanning sandbox row", err)
	}

	if err := json.Unmarshal([]byte(rawSpec), &sbx.Spec); err != nil {
		return nil, store.E("sqlite.scanSandbox", "database contained invalid spec json", err)
	}

	return sbx, nil
}

func scanSandboxRow(row *sql.Row) (*sandbox.Sandbox, error) {
	sbx, err := scanSandbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}

	return sbx, err
}

func scanSandboxes(rows *sql.Rows) ([]*sandbox.Sandbox, error) {
	sandboxes := []*sandbox.Sandbox{}
	for rows.Next() {
		sbx, err := scanSandbox(rows)
		if err != nil {
			return nil, store.Wrap("sqlite.scanSandboxes", "", err)
		}
		sandboxes = append(sandboxes, sbx)
	}
	if err := rows.Err(); err != nil {
		return nil, store.Wrap("sqlite.scanSandboxes", "error during rows iteration", err)
	}

	return sandboxes, nil
}

func (s *Store) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState) (*sandbox.Sandbox, error) {
	s.logger.DebugContext(ctx, "transitioning sandbox state", "sandboxID", sandboxID, "from", from, "state", to)

	const op = "sqlite.Store.UpdateSandboxState"
	msg := fmt.Sprintf("transitioning sandbox %s from %s to %s", sandboxID, from, to)

	if err := sandbox.CanTransition(from, to); err != nil {
		return nil, store.E(op, msg, err)
	}

	sbx, err := scanSandboxRow(s.db.QueryRowContext(ctx, UpdateSandboxStateQuery, to, sandboxID, from))
	if err != nil {
		// No row matched (id, from). The row is either absent or no longer in
		// `from`; one extra read tells which, and because the gate is in the
		// UPDATE's WHERE, nothing was written either way.
		if errors.Is(err, store.ErrNotFound) {
			if _, getErr := s.GetSandbox(ctx, sandboxID); getErr != nil {
				return nil, store.Wrap(op, msg, getErr)
			}
			return nil, store.E(op, msg, sandbox.ErrInvalidStateTransition)
		}
		return nil, store.Wrap(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox state changed", "sandboxID", sandboxID, "from", from, "state", to)

	return sbx, nil
}

func (s *Store) CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile) (*sandbox.Sandbox, error) {
	const op = "sqlite.Store.CreateSandbox"
	msg := fmt.Sprintf("creating sandbox for idempotency key %s", idempotencyKey)

	s.logger.DebugContext(ctx, "creating sandbox", "idempotencyKey", idempotencyKey)

	if err := spec.Validate(); err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	key, err := s.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return nil, store.Wrap(op, msg, err)
	}
	if key != nil {
		sbx, err := s.GetSandbox(ctx, key.SandboxID)
		if err != nil {
			return nil, store.Wrap(op, msg, err)
		}
		s.logger.InfoContext(ctx, "returning existing sandbox for idempotency key",
			"idempotencyKey", idempotencyKey, "sandboxID", sbx.SandboxID, "state", sbx.State)
		return sbx, nil
	}

	specJSON, err := spec.ToJSON()
	if err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	sandboxID := runtime.NewSandboxID()

	// The sandbox row and its key mapping are one unit: a crash between the two
	// inserts would leave a sandbox no key points at, so the client's retry
	// would create a second one — the exact outcome the key exists to prevent.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, store.E(op, msg, err)
	}
	defer tx.Rollback()

	sbx, err := scanSandboxRow(tx.QueryRowContext(ctx, InsertSandboxQuery, sandbox.Pending, specJSON, sandboxID))
	if err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	if _, err := scanIdempotencyKey(tx.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sbx.SandboxID)); err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, store.E(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox record created", "sandboxID", sbx.SandboxID, "state", sbx.State)

	return sbx, nil
}

func (s *Store) GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error) {
	s.logger.DebugContext(ctx, "reading sandbox", "sandboxID", sandboxID)

	sbx, err := scanSandboxRow(s.db.QueryRowContext(ctx, GetSandboxQuery, sandboxID))
	if err != nil {
		return nil, store.Wrap("sqlite.Store.GetSandbox",
			fmt.Sprintf("reading sandbox %s", sandboxID), err)
	}

	return sbx, nil
}

func (s *Store) GetSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error) {
	s.logger.DebugContext(ctx, "listing sandboxes")

	rows, err := s.db.QueryContext(ctx, GetSandboxesQuery)
	if err != nil {
		return nil, store.E("sqlite.Store.GetSandboxes", "listing sandboxes", err)
	}
	defer rows.Close()

	sandboxes, err := scanSandboxes(rows)
	if err != nil {
		return nil, store.Wrap("sqlite.Store.GetSandboxes", "listing sandboxes", err)
	}

	return sandboxes, nil
}

func (s *Store) Cleanup() error {
	if err := s.db.Close(); err != nil {
		return store.E("sqlite.Store.Cleanup", "closing database", err)
	}

	s.logger.Info("store closed")

	return nil
}
