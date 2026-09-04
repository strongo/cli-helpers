package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	helperselfupdate "github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/skillsync/reexec"
)

func TestAfterUpdateUsesResolvedInstalledExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	path := filepath.Join(t.TempDir(), "new-tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	callback := AfterUpdate(reexec.Runner{})
	if err := callback(context.Background(), helperselfupdate.AfterUpdate{Executable: helperselfupdate.ExecutableIdentity{Path: "/missing", ResolvedPath: path}}); err != nil {
		t.Fatal(err)
	}
}
