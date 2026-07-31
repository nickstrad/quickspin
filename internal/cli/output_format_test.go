package cli_test

import (
	"strings"
	"testing"
)

func TestUnsupportedOutputFormatIsRejected(t *testing.T) {
	_, _, err := execute(t, fakeAPI{}, "sandbox", "list", "--output", "toml")
	if err == nil || !strings.Contains(err.Error(), `unsupported output format "toml"`) {
		t.Fatalf("execute error = %v, want unsupported format error", err)
	}
}
