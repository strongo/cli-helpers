package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	helperselfupdate "github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/skillsync/reexec"
)

const bridgeChildEnvironment = "STRONGO_SKILLSYNC_BRIDGE_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(bridgeChildEnvironment) == "arguments" {
		_ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAfterUpdatePrefersResolvedInstalledExecutable(t *testing.T) {
	t.Setenv(bridgeChildEnvironment, "arguments")
	var output bytes.Buffer
	callback := AfterUpdate(reexec.Runner{Stderr: &output})
	if err := callback(context.Background(), helperselfupdate.AfterUpdate{Executable: helperselfupdate.ExecutableIdentity{Path: "/missing", ResolvedPath: os.Args[0]}}); err != nil {
		t.Fatal(err)
	}
	assertDefaultArgs(t, output.Bytes())
}

func TestAfterUpdateFallsBackToInvocationPath(t *testing.T) {
	t.Setenv(bridgeChildEnvironment, "arguments")
	var output bytes.Buffer
	callback := AfterUpdate(reexec.Runner{Stderr: &output})
	if err := callback(context.Background(), helperselfupdate.AfterUpdate{Executable: helperselfupdate.ExecutableIdentity{Path: os.Args[0]}}); err != nil {
		t.Fatal(err)
	}
	assertDefaultArgs(t, output.Bytes())
}

func assertDefaultArgs(t *testing.T, raw []byte) {
	t.Helper()
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("child output is not JSON: %v", err)
	}
	if len(got) != 2 || got[0] != "skills" || got[1] != "sync" {
		t.Fatalf("args = %#v, want []string{\"skills\", \"sync\"}", got)
	}
}
