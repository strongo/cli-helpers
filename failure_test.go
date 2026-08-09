package selfupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestFailureKind_String(t *testing.T) {
	cases := []struct {
		k    FailureKind
		want string
	}{
		{KindAmbiguous, "ambiguous"},
		{KindReleaseLookup, "release_lookup"},
		{KindDownload, "download"},
		{KindChecksum, "checksum"},
		{KindPermission, "permission"},
		{KindNonInteractive, "non_interactive"},
		{KindDowngrade, "downgrade"},
		{KindUnknownTag, "unknown_tag"},
		{KindUnsupportedPlatform, "unsupported_platform"},
		{KindUnexpected, "unexpected"},
		{FailureKind(999), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("FailureKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}

func TestFailure_ErrorAndUnwrap(t *testing.T) {
	inner := fmt.Errorf("write: %w", fs.ErrPermission)
	f := &Failure{Kind: KindPermission, Path: "/usr/local/bin/wb", Err: inner}

	if got := f.Error(); got == "" {
		t.Fatal("Error() returned empty string")
	}
	if !errors.Is(f, fs.ErrPermission) {
		t.Error("errors.Is(f, fs.ErrPermission) = false, want true (Unwrap must expose the inner error)")
	}
	if errors.Unwrap(f) != inner {
		t.Error("errors.Unwrap(f) did not return the wrapped error")
	}
}

func TestFailure_ErrorWithoutPath(t *testing.T) {
	f := &Failure{Kind: KindDownload, Err: errors.New("boom")}
	if got := f.Error(); got != "boom" {
		t.Errorf("Error() = %q, want %q", got, "boom")
	}
}

func TestKindOf(t *testing.T) {
	if got := KindOf(nil); got != KindUnexpected {
		t.Errorf("KindOf(nil) = %v, want KindUnexpected", got)
	}
	if got := KindOf(errors.New("plain")); got != KindUnexpected {
		t.Errorf("KindOf(plain error) = %v, want KindUnexpected", got)
	}
	if got := KindOf(&Failure{Kind: KindChecksum, Err: errors.New("x")}); got != KindChecksum {
		t.Errorf("KindOf(*Failure) = %v, want KindChecksum", got)
	}
	// KindOf must see through a wrapper, since Update sometimes returns a
	// *Failure produced by a caller-supplied Options.Confirm and other
	// times wraps one — either way KindOf must unwrap to it.
	wrapped := fmt.Errorf("update: %w", &Failure{Kind: KindDowngrade, Err: errors.New("x")})
	if got := KindOf(wrapped); got != KindDowngrade {
		t.Errorf("KindOf(wrapped *Failure) = %v, want KindDowngrade", got)
	}
}

func TestAction_String(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{ActionRedirected, "redirected"},
		{ActionAlreadyCurrent, "already_current"},
		{ActionUpdated, "updated"},
		{ActionAborted, "aborted"},
		{ActionPlanned, "planned"},
		{Action(999), "unknown"},
	}
	for _, c := range cases {
		if got := c.a.String(); got != c.want {
			t.Errorf("Action(%d).String() = %q, want %q", c.a, got, c.want)
		}
	}
}
