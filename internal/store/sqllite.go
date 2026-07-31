package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/nickstrad/quickspin/internal/runtime"
	_ "modernc.org/sqlite"
)

const (
	DefaultDBPath     = "control-plane.db"
	DefaultDriverType = "sqlite"
)

type SqlliteStore struct {
	db     *sql.DB
	logger *slog.Logger
}

var _ Store = (*SqlliteStore)(nil)

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

func NewSqlliteStore(ctx context.Context, dbFilePath string, dbDriverType string, logger *slog.Logger) (*SqlliteStore, error) {

	if dbFilePath == "" {
		dbFilePath = DefaultDBPath
	}

	if logger == nil {
		return nil, E("store.NewSqlliteStore", "logger is required", nil)
	}

	if dbDriverType == "" {
		dbDriverType = DefaultDriverType
	}

	// foreign_keys is off by default in SQLite, so without it the FK on
	// idempotency_keys.sandbox_id is documentation rather than a constraint.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", dbFilePath)
	db, err := sql.Open(dbDriverType, dsn)
	if err != nil {
		return nil, E("store.NewSqlliteStore",
			fmt.Sprintf("opening %s database %s", dbDriverType, dbFilePath), err)
	}

	db.SetMaxOpenConns(1)    // Only allow 1 write connection at a time to prevent locks
	db.SetMaxIdleConns(1)    // Keep that connection open
	db.SetConnMaxLifetime(0) // Reuse connections indefinitely

	schemas := []string{SandboxSchema, IdempotencyKeySchema}
	for _, schema := range schemas {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			_ = db.Close()
			return nil, E("store.NewSqlliteStore", "executing schema", err)
		}
	}

	logger.InfoContext(ctx, "store opened", "path", dbFilePath, "driver", dbDriverType)

	return &SqlliteStore{
		db:     db,
		logger: logger,
	}, nil
}

func (s *SqlliteStore) GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*IdempotencyKey, error) {

	k, err := scanIdempotencyKey(s.db.QueryRowContext(ctx, GetIdempotencyKeyQuery, idempotencyKey))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.logger.DebugContext(ctx, "idempotency key miss", "idempotencyKey", idempotencyKey)
			return nil, nil
		}
		return nil, Wrap("store.SqlliteStore.GetIdempotencyKey",
			fmt.Sprintf("reading idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "idempotency key hit", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

func scanIdempotencyKey(row *sql.Row) (*IdempotencyKey, error) {
	k := &IdempotencyKey{}

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
			return nil, ErrNotFound
		}
		return nil, E("store.scanIdempotencyKey", "scanning idempotency key row", err)
	}

	return k, nil
}

func (s *SqlliteStore) CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*IdempotencyKey, error) {
	k, err := scanIdempotencyKey(s.db.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sandboxID))
	if err != nil {
		return nil, Wrap("store.SqlliteStore.CreateIdempotencyKey",
			fmt.Sprintf("recording idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "recorded idempotency key", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSandbox(scanner rowScanner) (*Sandbox, error) {
	sandbox := &Sandbox{}
	var rawSpec string

	err := scanner.Scan(
		&sandbox.ID,
		&sandbox.SandboxID,
		&sandbox.State,
		&rawSpec,
		&sandbox.CreatedAt,
		&sandbox.UpdatedAt,
	)
	if err != nil {
		return nil, E("store.scanSandbox", "scanning sandbox row", err)
	}

	if err := json.Unmarshal([]byte(rawSpec), &sandbox.Spec); err != nil {
		return nil, E("store.scanSandbox", "database contained invalid spec json", err)
	}

	return sandbox, nil
}

func scanSandboxRow(row *sql.Row) (*Sandbox, error) {
	sandbox, err := scanSandbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	return sandbox, err
}

func scanSandboxes(rows *sql.Rows) ([]*Sandbox, error) {
	sandboxes := []*Sandbox{}
	for rows.Next() {
		sandbox, err := scanSandbox(rows)
		if err != nil {
			return nil, Wrap("store.scanSandboxes", "", err)
		}
		sandboxes = append(sandboxes, sandbox)
	}
	if err := rows.Err(); err != nil {
		return nil, Wrap("store.scanSandboxes", "error during rows iteration", err)
	}

	return sandboxes, nil
}

func (s *SqlliteStore) UpdateSandboxState(ctx context.Context, sandboxID string, from, to TaskState) (*Sandbox, error) {
	s.logger.DebugContext(ctx, "transitioning sandbox state", "sandboxID", sandboxID, "from", from, "state", to)

	const op = "store.SqlliteStore.UpdateSandboxState"
	msg := fmt.Sprintf("transitioning sandbox %s from %s to %s", sandboxID, from, to)

	if err := canTransition(from, to); err != nil {
		return nil, E(op, msg, err)
	}

	sandbox, err := scanSandboxRow(s.db.QueryRowContext(ctx, UpdateSandboxStateQuery, to, sandboxID, from))
	if err != nil {
		// No row matched (id, from). The row is either absent or no longer in
		// `from`; one extra read tells which, and because the gate is in the
		// UPDATE's WHERE, nothing was written either way.
		if errors.Is(err, ErrNotFound) {
			if _, getErr := s.GetSandbox(ctx, sandboxID); getErr != nil {
				return nil, Wrap(op, msg, getErr)
			}
			return nil, E(op, msg, ErrInvalidStateTransition)
		}
		return nil, Wrap(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox state changed", "sandboxID", sandboxID, "from", from, "state", to)

	return sandbox, nil
}

func (s *SqlliteStore) CreateSandbox(ctx context.Context, idempotencyKey string, spec SpecFile) (*Sandbox, error) {
	const op = "store.SqlliteStore.CreateSandbox"
	msg := fmt.Sprintf("creating sandbox for idempotency key %s", idempotencyKey)

	s.logger.DebugContext(ctx, "creating sandbox", "idempotencyKey", idempotencyKey)

	if err := spec.Validate(); err != nil {
		return nil, Wrap(op, "", err)
	}

	key, err := s.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return nil, Wrap(op, "", err)
	}
	if key != nil {
		sandbox, err := s.GetSandbox(ctx, key.SandboxID)
		if err != nil {
			return nil, Wrap(op, "", err)
		}
		s.logger.InfoContext(ctx, "returning existing sandbox for idempotency key",
			"idempotencyKey", idempotencyKey, "sandboxID", sandbox.SandboxID, "state", sandbox.State)
		return sandbox, nil
	}

	specJSON, err := spec.ToJSON()
	if err != nil {
		return nil, Wrap(op, "", err)
	}

	sandboxID := runtime.NewSandboxID()

	// The sandbox row and its key mapping are one unit: a crash between the two
	// inserts would leave a sandbox no key points at, so the client's retry
	// would create a second one — the exact outcome the key exists to prevent.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, E(op, msg, err)
	}
	defer tx.Rollback()

	sandbox, err := scanSandboxRow(tx.QueryRowContext(ctx, InsertSandboxQuery, Pending, specJSON, sandboxID))
	if err != nil {
		return nil, Wrap(op, msg, err)
	}

	if _, err := scanIdempotencyKey(tx.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sandbox.SandboxID)); err != nil {
		return nil, Wrap(op, msg, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, E(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox record created", "sandboxID", sandbox.SandboxID, "state", sandbox.State)

	return sandbox, nil
}

func (s *SqlliteStore) GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error) {
	s.logger.DebugContext(ctx, "reading sandbox", "sandboxID", sandboxID)

	sandbox, err := scanSandboxRow(s.db.QueryRowContext(ctx, GetSandboxQuery, sandboxID))
	if err != nil {
		return nil, Wrap("store.SqlliteStore.GetSandbox",
			fmt.Sprintf("reading sandbox %s", sandboxID), err)
	}

	return sandbox, nil
}

func (s *SqlliteStore) GetSandboxes(ctx context.Context) ([]*Sandbox, error) {
	s.logger.DebugContext(ctx, "listing sandboxes")

	rows, err := s.db.QueryContext(ctx, GetSandboxesQuery)
	if err != nil {
		return nil, E("store.sqllite.GetSandboxes", "listing sandboxes", err)
	}
	defer rows.Close()

	sandboxes, err := scanSandboxes(rows)
	if err != nil {
		return nil, Wrap("store.SqlliteStore.GetSandboxes", "listing sandboxes", err)
	}

	return sandboxes, nil
}

func (s *SqlliteStore) Cleanup() error {
	if err := s.db.Close(); err != nil {
		return E("store.SqlliteStore.Cleanup", "closing database", err)
	}

	s.logger.Info("store closed")

	return nil
}
