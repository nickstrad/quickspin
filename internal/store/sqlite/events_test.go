package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
	"github.com/nickstrad/quickspin/internal/store/storetest"
)

// The behavior of the event log is in storetest, which every store must pass.
// What is left here is what only this store can be wrong about: the schema and
// the connection's pragmas.

// Nothing reachable through the store can write an orphan event, so the insert
// goes in raw. It fails if the foreign_keys pragma is ever dropped from the DSN.
func TestAnEventForAnUnknownSandboxIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	newTestStore(t, path)

	raw, err := sql.Open(sqlite.DefaultDriverType, sqlite.DSN(path))
	if err != nil {
		t.Fatalf("sql.Open(%s) error = %v, want nil", path, err)
	}
	t.Cleanup(func() { raw.Close() })

	_, err = raw.ExecContext(context.Background(), sqlite.InsertEventQuery,
		"sbx_never_existed", sandbox.Pending, sandbox.Running, time.Now(), "started")
	if err == nil {
		t.Error("appending an event for an unknown sandbox error = nil, want a foreign key violation")
	}
}

func TestEventReadsHonorCanceledContext(t *testing.T) {
	st := newTestStore(t, ":memory:")

	image := "alpine:3.20"
	sbx, err := st.CreateSandbox(context.Background(), "canceled", sandbox.SpecFile{Image: &image}, storetest.TestExpiry())
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := st.GetSandboxEvents(ctx, sbx.SandboxID); !errors.Is(err, context.Canceled) {
		t.Errorf("GetSandboxEvents(canceled ctx) error = %v, want context.Canceled", err)
	}
}
