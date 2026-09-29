package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "/home/u", nil }

	tests := []struct{ in, want string }{
		{"~", "/home/u"},
		{"~/", "/home/u"},
		{"~/a/b", filepath.Join("/home/u", "a", "b")},
		{"~x", "~x"},
		{"/abs", "/abs"},
		{"rel", "rel"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ExpandHome(tt.in); got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExpandHome_HomeUnavailable(t *testing.T) {
	orig := userHomeDir
	t.Cleanup(func() { userHomeDir = orig })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	for _, in := range []string{"~", "~/x"} {
		if got := ExpandHome(in); got != in {
			t.Errorf("ExpandHome(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, path string
		want       bool
	}{
		{"directory", dir, true},
		{"file", file, false},
		{"missing", filepath.Join(dir, "nope"), false},
	}
	for _, tt := range tests {
		got, err := DirExists(tt.path)
		if err != nil || got != tt.want {
			t.Errorf("%s: DirExists = %v, %v; want %v, nil", tt.name, got, err, tt.want)
		}
	}
}

func TestDirExists_StatError(t *testing.T) {
	orig := stat
	t.Cleanup(func() { stat = orig })
	boom := errors.New("boom")
	stat = func(string) (os.FileInfo, error) { return nil, boom }

	got, err := DirExists("x")
	if got || !errors.Is(err, boom) {
		t.Errorf("DirExists = %v, %v; want false, boom", got, err)
	}
}
