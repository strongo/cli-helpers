package daemonlifecycle

import (
	"context"
	"errors"
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
	defer func() { _ = file.Close() }()
	if err := ProtectOwnerOnlyFile(file); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(file); err != nil {
		t.Fatal(err)
	}
	if err := Lock(context.Background(), file, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	competitor, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = competitor.Close() }()
	if locked, err := TryLock(competitor); err != nil || locked {
		t.Fatalf("competing lock = %t, %v; want busy", locked, err)
	}
	if err := Unlock(file); err != nil {
		t.Fatal(err)
	}
}

func TestLockCancellationRetryAndError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.lock")
	owner, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	if locked, err := TryLock(owner); err != nil || !locked {
		t.Fatalf("owner lock = %t, %v", locked, err)
	}
	competitor, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = competitor.Close() }()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Lock(cancelled, competitor, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Lock error = %v", err)
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = Unlock(owner)
	}()
	if err := Lock(context.Background(), competitor, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := Unlock(competitor); err != nil {
		t.Fatal(err)
	}
	closed, err := os.OpenFile(filepath.Join(t.TempDir(), "closed.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Lock(context.Background(), closed, 0); err == nil {
		t.Fatal("Lock accepted a closed file")
	}
}
