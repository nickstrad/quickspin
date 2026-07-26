package cli_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/cli"
	"github.com/nickstrad/quickspin/internal/runtime"
)

const (
	testID  = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"
	otherID = "sbx_2c1d0e9f-8a7b-4c6d-9e5f-4a3b2c1d0e9f"
)

var testTime = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

func execute(t *testing.T, rt runtime.Runtime, args ...string) (string, string, error) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := cli.NewCommand(rt)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}
