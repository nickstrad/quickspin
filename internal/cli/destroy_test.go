package cli_test

import (
	"context"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestDestroyWritesMachineReadableConfirmation(t *testing.T) {
	var gotID string
	rt := runtimetest.Fake{
		DestroyFn: func(_ context.Context, id string) error {
			gotID = id
			return nil
		},
	}

	stdout, _, err := execute(t, rt, "sandbox", "destroy", testID, "-o", "json")
	if err != nil {
		t.Fatalf("execute destroy error = %v, want nil", err)
	}
	if gotID != testID {
		t.Errorf("Destroy id = %q, want %q", gotID, testID)
	}

	want := "{\n" +
		"  \"id\": \"" + testID + "\",\n" +
		"  \"status\": \"destroyed\"\n" +
		"}\n"
	if stdout != want {
		t.Errorf("destroy output =\n%s\nwant\n%s", stdout, want)
	}
}
