package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
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
		expires_at DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

// The `state = ?` predicate is the transition gate: matching the expected
// current state and writing the new one in a single statement is atomic, so
// two writers cannot both observe `running` and both move out of it.
const UpdateSandboxStateQuery = `
UPDATE sandboxes
SET state = ?, updated_at = CURRENT_TIMESTAMP
WHERE platform_id = ? AND state = ?
RETURNING id, platform_id, state, spec, created_at, updated_at, expires_at;`

const InsertSandboxQuery = `
INSERT INTO sandboxes (state, spec, platform_id, expires_at)
VALUES (?, ?, ?, ?)
RETURNING id, platform_id, state, spec, created_at, updated_at, expires_at;`

const GetSandboxQuery = `
SELECT id, platform_id, state, spec, created_at, updated_at, expires_at
FROM sandboxes
WHERE platform_id = ?;`

const GetSandboxesQuery = `
SELECT id, platform_id, state, spec, created_at, updated_at, expires_at
FROM sandboxes;`

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
FROM idempotency_keys
WHERE idempotency_key = ?;`

const EventsSchema = `
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sandbox_id TEXT NOT NULL,
	from_state TEXT NOT NULL,
	to_state TEXT NOT NULL,
	at DATETIME NOT NULL,
	reason TEXT NOT NULL,
	FOREIGN KEY(sandbox_id) REFERENCES sandboxes(platform_id)
);`

// SQLite does not index foreign key columns automatically, and the log only
// ever grows, so without this a per-sandbox read scans the whole table.
const EventsSandboxIDIndexSchema = `
CREATE INDEX IF NOT EXISTS events_sandbox_id ON events(sandbox_id);`

// `at` is written rather than defaulted so an event carries the same instant as
// the row it describes.
const InsertEventQuery = `INSERT INTO events
(sandbox_id, from_state, to_state, at, reason)
 VALUES (?, ?, ?, ?, ?);`

// Ordered by id, not at: the log replays in the order it was appended, and two
// transitions can share a timestamp at the schema's resolution.
const GetSandboxEventsQuery = `SELECT
	id, sandbox_id, from_state, to_state, at, reason
FROM events
WHERE sandbox_id = ?
ORDER BY id;`

var schemas = []string{SandboxSchema, IdempotencyKeySchema, EventsSchema, EventsSandboxIDIndexSchema}

// DSN builds the connection string New opens. foreign_keys is off by default
// in SQLite, so without it the FKs on idempotency_keys.sandbox_id and
// events.sandbox_id are documentation rather than constraints.
func DSN(dbFilePath string) string {
	return fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", dbFilePath)
}

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

	db, err := sql.Open(dbDriverType, DSN(dbFilePath))
	if err != nil {
		return nil, store.E("sqlite.New",
			fmt.Sprintf("opening %s database %s", dbDriverType, dbFilePath), err)
	}

	db.SetMaxOpenConns(1)    // Only allow 1 write connection at a time to prevent locks
	db.SetMaxIdleConns(1)    // Keep that connection open
	db.SetConnMaxLifetime(0) // Reuse connections indefinitely

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

	k, err := scanRow(s.db.QueryRowContext(ctx, GetIdempotencyKeyQuery, idempotencyKey), scanIdempotencyKey)
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

func (s *Store) CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*sandbox.IdempotencyKey, error) {
	k, err := scanRow(s.db.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sandboxID), scanIdempotencyKey)
	if err != nil {
		return nil, store.Wrap("sqlite.Store.CreateIdempotencyKey",
			fmt.Sprintf("recording idempotency key %s", idempotencyKey), err)
	}

	s.logger.DebugContext(ctx, "recorded idempotency key", "idempotencyKey", idempotencyKey, "sandboxID", k.SandboxID)

	return k, nil
}

func (s *Store) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error) {
	s.logger.DebugContext(ctx, "transitioning sandbox state", "sandboxID", sandboxID, "from", from, "state", to)

	const op = "sqlite.Store.UpdateSandboxState"
	msg := fmt.Sprintf("transitioning sandbox %s from %s to %s", sandboxID, from, to)

	if err := sandbox.CanTransition(from, to); err != nil {
		return nil, store.E(op, msg, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, store.E(op, msg, err)
	}
	defer tx.Rollback()

	sbx, err := scanRow(tx.QueryRowContext(ctx, UpdateSandboxStateQuery, to, sandboxID, from), scanSandbox)
	if err != nil {
		// No row matched (id, from). The row is either absent or no longer in
		// `from`; one extra read tells which, and because the gate is in the
		// UPDATE's WHERE, nothing was written either way. The read goes through
		// tx because the pool holds a single connection, which this
		// transaction owns until it ends.
		if errors.Is(err, store.ErrNotFound) {
			if _, getErr := scanRow(tx.QueryRowContext(ctx, GetSandboxQuery, sandboxID), scanSandbox); getErr != nil {
				return nil, store.Wrap(op, msg, getErr)
			}
			return nil, store.E(op, msg, sandbox.ErrInvalidStateTransition)
		}
		return nil, store.Wrap(op, msg, err)
	}

	if err := s.appendEvent(ctx, tx, &events.Event{
		SandboxID: sbx.SandboxID,
		FromState: from,
		ToState:   to,
		At:        sbx.UpdatedAt,
		Reason:    reason,
	}); err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, store.E(op, msg, err)
	}

	s.logger.InfoContext(ctx, "sandbox state changed", "sandboxID", sandboxID, "from", from, "state", to, "reason", reason)

	return sbx, nil
}

// appendEvent writes the event for a transition inside the caller's
// transaction, so the row and its history commit together or neither does.
func (s *Store) appendEvent(ctx context.Context, tx *sql.Tx, e *events.Event) error {
	const op = "sqlite.Store.appendEvent"
	msg := fmt.Sprintf("appending the %s -> %s event for sandbox %s", e.FromState, e.ToState, e.SandboxID)

	if err := e.Validate(); err != nil {
		return store.Wrap(op, msg, err)
	}

	if _, err := tx.ExecContext(ctx, InsertEventQuery,
		e.SandboxID, e.FromState, e.ToState, e.At, e.Reason); err != nil {
		return store.Wrap(op, msg, err)
	}

	return nil
}

func (s *Store) CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error) {
	const op = "sqlite.Store.CreateSandbox"
	msg := fmt.Sprintf("creating sandbox for idempotency key %s", idempotencyKey)

	s.logger.DebugContext(ctx, "creating sandbox", "idempotencyKey", idempotencyKey)

	if err := spec.Validate(); err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	if expiresAt.IsZero() {
		return nil, store.E(op, msg, store.ErrMissingExpiry)
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

	sbx, err := scanRow(tx.QueryRowContext(ctx, InsertSandboxQuery, sandbox.Pending, specJSON, sandboxID, expiresAt), scanSandbox)
	if err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	if _, err := scanRow(tx.QueryRowContext(ctx, InsertIdempotencyKeyQuery, idempotencyKey, sbx.SandboxID), scanIdempotencyKey); err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	// The first event has no from state: the sandbox is coming from nothing, and
	// a replay has to start somewhere.
	if err := s.appendEvent(ctx, tx, &events.Event{
		SandboxID: sbx.SandboxID,
		ToState:   sbx.State,
		At:        sbx.CreatedAt,
		Reason:    "sandbox record created",
	}); err != nil {
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

	sbx, err := scanRow(s.db.QueryRowContext(ctx, GetSandboxQuery, sandboxID), scanSandbox)
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

	sandboxes, err := scanRows(rows, scanSandbox)
	if err != nil {
		return nil, store.Wrap("sqlite.Store.GetSandboxes", "listing sandboxes", err)
	}

	return sandboxes, nil
}

func (s *Store) GetSandboxEvents(ctx context.Context, sandboxID string) ([]*events.Event, error) {
	s.logger.DebugContext(ctx, "listing sandbox events", "sandboxID", sandboxID)

	return s.listEvents(ctx, "sqlite.Store.GetSandboxEvents",
		fmt.Sprintf("listing events for sandbox %s", sandboxID), GetSandboxEventsQuery, sandboxID)
}

func (s *Store) listEvents(ctx context.Context, op, msg, query string, args ...any) ([]*events.Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, store.E(op, msg, err)
	}
	defer rows.Close()

	evts, err := scanRows(rows, scanEvent)
	if err != nil {
		return nil, store.Wrap(op, msg, err)
	}

	return evts, nil
}

func (s *Store) Cleanup() error {
	if err := s.db.Close(); err != nil {
		return store.E("sqlite.Store.Cleanup", "closing database", err)
	}

	s.logger.Info("store closed")

	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIdempotencyKey(scanner rowScanner) (*sandbox.IdempotencyKey, error) {
	k := &sandbox.IdempotencyKey{}

	err := scanner.Scan(
		&k.ID,
		&k.SandboxID,
		&k.Key,
		&k.CreatedAt,
		&k.UpdatedAt,
	)
	if err != nil {
		return nil, store.E("sqlite.scanIdempotencyKey", "scanning idempotency key row", err)
	}

	return k, nil
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
		&sbx.ExpiresAt,
	)
	if err != nil {
		return nil, store.E("sqlite.scanSandbox", "scanning sandbox row", err)
	}

	if err := json.Unmarshal([]byte(rawSpec), &sbx.Spec); err != nil {
		return nil, store.E("sqlite.scanSandbox", "database contained invalid spec json", err)
	}

	return sbx, nil
}

func scanEvent(scanner rowScanner) (*events.Event, error) {
	e := &events.Event{}

	err := scanner.Scan(
		&e.ID,
		&e.SandboxID,
		&e.FromState,
		&e.ToState,
		&e.At,
		&e.Reason,
	)
	if err != nil {
		return nil, store.E("sqlite.scanEvent", "scanning event row", err)
	}

	return e, nil
}

func scanRow[T any](row *sql.Row, scan func(rowScanner) (*T, error)) (*T, error) {
	v, err := scan(row)
	// A bare sentinel, not E: a missing row is an expected outcome for lookups,
	// so no stack capture.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}

	return v, err
}

func scanRows[T any](rows *sql.Rows, scan func(rowScanner) (*T, error)) ([]*T, error) {
	items := []*T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, store.Wrap("sqlite.scanRows", "", err)
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return nil, store.Wrap("sqlite.scanRows", "error during rows iteration", err)
	}

	return items, nil
}
