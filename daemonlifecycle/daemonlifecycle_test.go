package daemonlifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerOnlyPathAndLockJourney(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ProtectOwnerOnly(dir); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnly(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lifecycle.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := ProtectOwnerOnly(path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnly(path); err != nil {
		t.Fatal(err)
	}
	if err := Lock(context.Background(), file, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	competitor, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()
	if locked, err := TryLock(competitor); err != nil || locked {
		t.Fatalf("competing lock = %t, %v; want busy", locked, err)
	}
	if err := Unlock(file); err != nil {
		t.Fatal(err)
	}
}
