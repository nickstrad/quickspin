package cli_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/httpapi"
)

func TestRemovePathPassesTheTargetAndStaysSilent(t *testing.T) {
	var gotID, gotPath string
	api := fakeAPI{
		RemovePathFn: func(_ context.Context, id, path string) error {
			gotID, gotPath = id, path
			return nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "rm", testID, "/work/build")
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
	api := fakeAPI{
		RemovePathFn: func(context.Context, string, string) error {
			return &client.Error{
				Status:  http.StatusNotFound,
				Code:    httpapi.CodeNotFound,
				Message: "path not found in the sandbox",
			}
		},
	}

	_, _, err := execute(t, api, "sandbox", "rm", testID, "/work/build")
	if !client.HasCode(err, httpapi.CodeNotFound) {
		t.Fatalf("execute rm error = %v, want the not_found code to survive wrapping", err)
	}
}
