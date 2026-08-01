package cli_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/client"
)

func TestRemovePathPassesTheTargetAndStaysSilent(t *testing.T) {
	var gotID, gotPath string
	fake := fakeAPI{
		RemovePathFn: func(_ context.Context, id, path string) error {
			gotID, gotPath = id, path
			return nil
		},
	}

	stdout, _, err := execute(t, fake, "sandbox", "rm", testID, "/work/build")
	if err != nil {
		t.Fatalf("execute rm error = %v, want nil", err)
	}
	if gotID != testID || gotPath != "/work/build" {
		t.Errorf("RemovePath target = %q:%s, want %q:/work/build", gotID, gotPath, testID)
	}
	if stdout != "" {
		t.Errorf("rm output = %q, want silent success", stdout)
	}
}

func TestRemovePathPreservesTheAPIErrorCode(t *testing.T) {
	fake := fakeAPI{
		RemovePathFn: func(context.Context, string, string) error {
			return &client.Error{
				Status:  http.StatusNotFound,
				Code:    api.CodeNotFound,
				Message: "path not found in the sandbox",
			}
		},
	}

	_, _, err := execute(t, fake, "sandbox", "rm", testID, "/work/build")
	if !client.HasCode(err, api.CodeNotFound) {
		t.Fatalf("execute rm error = %v, want the not_found code to survive wrapping", err)
	}
}
