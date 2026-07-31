package cli_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/runtime"
)

func TestInspectWritesYAML(t *testing.T) {
	var gotID string
	api := fakeAPI{
		InspectFn: func(_ context.Context, id string) (runtime.Info, error) {
			gotID = id
			return runtime.Info{
				ID:        id,
				State:     runtime.StateRunning,
				CreatedAt: testTime,
			}, nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "inspect", testID, "--output=yaml")
	if err != nil {
		t.Fatalf("execute inspect error = %v, want nil", err)
	}
	if gotID != testID {
		t.Errorf("Inspect id = %q, want %q", gotID, testID)
	}

	want := "" +
		"id: " + testID + "\n" +
		"state: running\n" +
		"created_at: 2026-07-25T12:00:00Z\n"
	if stdout != want {
		t.Errorf("inspect output =\n%s\nwant\n%s", stdout, want)
	}
}

// Sentinels do not survive the wire: what the CLI gets back is the envelope's
// code, and wrapping with %w is what keeps it reachable through the command's
// own "inspect sandbox ..." message.
func TestCommandPreservesTheAPIErrorCode(t *testing.T) {
	api := fakeAPI{
		InspectFn: func(context.Context, string) (runtime.Info, error) {
			return runtime.Info{}, &client.Error{
				Status:  http.StatusNotFound,
				Code:    httpapi.CodeNotFound,
				Message: "sandbox not found",
			}
		},
	}

	_, _, err := execute(t, api, "sandbox", "inspect", testID)
	if !client.HasCode(err, httpapi.CodeNotFound) {
		t.Fatalf("execute inspect error = %v, want the not_found code to survive wrapping", err)
	}
}
