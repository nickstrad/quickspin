package cli_test

import (
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestUnsupportedOutputFormatIsRejected(t *testing.T) {
	_, _, err := execute(t, runtimetest.Fake{}, "sandbox", "list", "--output", "toml")
	if err == nil || !strings.Contains(err.Error(), `unsupported output format "toml"`) {
		t.Fatalf("execute error = %v, want unsupported format error", err)
	}
}
