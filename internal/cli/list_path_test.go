package cli_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestListPathPassesTheTargetAndWritesStableJSON(t *testing.T) {
	infos := []runtime.FileInfo{
		{Path: "/work/main.go", Size: 13, Mode: 0o640},
		{Path: "/work/logs", Mode: fs.ModeDir | 0o750, IsDir: true},
	}
	var gotID, gotPath string
	rt := runtimetest.Fake{
		ListDirFn: func(_ context.Context, id, path string) ([]runtime.FileInfo, error) {
			gotID, gotPath = id, path
			return infos, nil
		},
	}

	stdout, _, err := execute(t, rt, "sandbox", "ls", testID, "/work", "--output", "json")
	if err != nil {
		t.Fatalf("execute ls error = %v, want nil", err)
	}
	if gotID != testID || gotPath != "/work" {
		t.Errorf("ListDir target = %q:%s, want %q:/work", gotID, gotPath, testID)
	}

	var got []struct {
		Path  string      `json:"path"`
		Size  int64       `json:"size"`
		Mode  fs.FileMode `json:"mode"`
		IsDir bool        `json:"is_dir"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode ls output %q: %v", stdout, err)
	}
	if len(got) != 2 || got[0].Path != "/work/logs" || got[1].Path != "/work/main.go" {
		t.Fatalf("ls output = %+v, want entries sorted by path", got)
	}
	if !got[0].IsDir || got[0].Mode != fs.ModeDir|0o750 {
		t.Errorf("logs output = %+v, want directory mode and flag", got[0])
	}
	if infos[0].Path != "/work/main.go" {
		t.Error("ls sorted the runtime-owned slice in place, want a copied slice")
	}
}
