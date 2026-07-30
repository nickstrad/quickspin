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

const SandboxSchema = `CREATE TABLE IF NOT EXISTS sandboxes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform_id TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL,
		spec TEXT NOT NULL CHECK(json_valid(spec)),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

const IdempotencyKeySchema = `
CREATE TABLE IF NOT EXISTS idempotency_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sandbox_id INTEGER NOT NULL,
		idempotency_key TEXT NOT NULL UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(sandbox_id) REFERENCES sandboxes(id) ON DELETE CASCADE
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
WHERE id = ? AND state = ?
RETURNING id, platform_id, state, spec, created_at, updated_at;`

const InsertSandboxQuery = `
INSERT INTO 
	sandboxes (state, spec, platform_id)
VALUES (?, ?, ?)
RETURNING id, platform_id, state, spec, created_at, updated_at;`

const GetSandboxQuery = `
SELECT id, platform_id, state, spec, created_at, updated_at
FROM sandboxes
WHERE id = ?;`

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

func (s *SqlliteStore) CreateIdempotencyKey(ctx context.Context, idempotencyKey string, sandboxID int) (*IdempotencyKey, error) {
	k, err := scanIdempotencyKey(s.db.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sandboxID))
	if err != nil {
		return nil, Wrap("store.SqlliteStore.CreateIdempotencyKey",
			fmt.Sprintf("recording idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "recorded idempotency key", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

// scanSandbox decodes the spec with plain json.Unmarshal, not parseSpecFile:
// the column is JSON by contract (written by ToJSON, CHECK(json_valid)), and the
// lenient parser would reject a legitimately stored all-defaults spec.
func scanSandbox(row *sql.Row) (*Sandbox, error) {
	sbx := &Sandbox{}
	var rawSpec string

	err := row.Scan(
		&sbx.ID,
		&sbx.PlatformID,
		&sbx.State,
		&rawSpec,
		&sbx.CreatedAt,
		&sbx.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, E("store.scanSandbox", "scanning sandbox row", err)
	}

	if err := json.Unmarshal([]byte(rawSpec), &sbx.Spec); err != nil {
		return nil, E("store.scanSandbox", "database contained invalid spec json", err)
	}

	return sbx, nil
}

func (s *SqlliteStore) UpdateSandboxState(ctx context.Context, from string, to string, sandboxID string) (*Sandbox, error) {
	s.logger.DebugContext(ctx, "transitioning sandbox state", "sandboxID", sandboxID, "from", from, "state", to)

	const op = "store.SqlliteStore.UpdateSandboxState"
	msg := fmt.Sprintf("transitioning sandbox %s from %s to %s", sandboxID, from, to)

	if err := canTransition(from, to); err != nil {
		return nil, E(op, msg, err)
	}

	sbx, err := scanSandbox(s.db.QueryRowContext(ctx, UpdateSandboxStateQuery, to, sandboxID, from))
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

	return sbx, nil
}

func (s *SqlliteStore) CreateSandbox(ctx context.Context, idempotencyKey string, spec string) (*Sandbox, error) {
	const op = "store.SqlliteStore.CreateSandbox"
	msg := fmt.Sprintf("creating sandbox for idempotency key %s", idempotencyKey)

	s.logger.DebugContext(ctx, "creating sandbox", "idempotencyKey", idempotencyKey)

	// Errors from sibling store methods and parseSpecFile already carry their
	// own op and context (including the key), so add only this op above them.
	key, err := s.GetIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return nil, Wrap(op, "", err)
	}
	if key != nil {
		sbx, err := s.GetSandbox(ctx, key.SandboxID)
		if err != nil {
			return nil, Wrap(op, "", err)
		}
		// A retry is a read: the caller gets the record as it is now, not a
		// replay of the original create.
		s.logger.InfoContext(ctx, "returning existing sandbox for idempotency key",
			"idempotencyKey", idempotencyKey, "sandboxID", sbx.PlatformID, "state", sbx.State)
		return sbx, nil
	}

	specObj, err := parseSpecFile(spec)
	if err != nil {
		return nil, Wrap(op, "", err)
	}

	specJSON, err := specObj.ToJSON()
	if err != nil {
		return nil, Wrap(op, "", err)
	}

	platformID := runtime.NewSandboxID()

	// The sandbox row and its key mapping are one unit: a crash between the two
	// inserts would leave a sandbox no key points at, so the client's retry
	// would create a second one — the exact outcome the key exists to prevent.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, E(op, msg, err)
	}
	defer tx.Rollback()

	sbx, err := scanSandbox(tx.QueryRowContext(ctx, InsertSandboxQuery, Pending, specJSON, platformID))
	if err != nil {
		return nil, Wrap(op, msg, err)
	}

	if _, err := scanIdempotencyKey(tx.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sbx.ID)); err != nil {
		return nil, Wrap(op, msg, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, E(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox record created", "sandboxID", sbx.PlatformID, "state", sbx.State)

	return sbx, nil
}

func (s *SqlliteStore) GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error) {
	s.logger.DebugContext(ctx, "reading sandbox", "sandboxID", sandboxID)

	sbx, err := scanSandbox(s.db.QueryRowContext(ctx, GetSandboxQuery, sandboxID))
	if err != nil {
		return nil, Wrap("store.SqlliteStore.GetSandbox",
			fmt.Sprintf("reading sandbox %s", sandboxID), err)
	}

	return sbx, nil
}

func (s *SqlliteStore) Cleanup() error {
	if err := s.db.Close(); err != nil {
		return E("store.SqlliteStore.Cleanup", "closing database", err)
	}

	s.logger.Info("store closed")

	return nil
}
