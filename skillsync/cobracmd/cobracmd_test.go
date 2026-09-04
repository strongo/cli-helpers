package cobracmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
)

type mappedError struct{ err error }

func (e mappedError) Error() string { return e.err.Error() }
func (e mappedError) Unwrap() error { return e.err }

type testMapper struct{}

func (testMapper) Failure(err error) error { return mappedError{err} }
func (testMapper) Conflict(report skillsync.Report) error {
	return mappedError{errors.New("host conflict")}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }

func executeWith(t *testing.T, cfg skillsync.Config, opts CommandOptions, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	cmd := New(cfg, opts)
	cmd.SetContext(ctx)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

type failingResolver struct {
	err   error
	calls int
}

func (r *failingResolver) Resolve(context.Context, skillsync.Bundle) (skillsync.Bundle, error) {
	r.calls++
	return skillsync.Bundle{}, r.err
}

func commandConfig(t *testing.T) skillsync.Config {
	t.Helper()
	content := fstest.MapFS{"tool-install/SKILL.md": &fstest.MapFile{Data: []byte("skill")}}
	digest, err := skillsync.Digest(content)
	if err != nil {
		t.Fatal(err)
	}
	b, err := skillsync.EmbeddedBundle(skillsync.BundleDescriptor{Plugin: skillsync.PluginIdentity{Publisher: "strongo", Name: "tool-plugin"}, Source: skillsync.Source{Repository: "github.com/strongo/tool-plugin", Path: "skills", Revision: "0123456789012345678901234567890123456789", Version: "1.0.0", Digest: digest}}, content)
	if err != nil {
		t.Fatal(err)
	}
	return skillsync.Config{CLI: skillsync.Identity{Publisher: "strongo", Name: "tool"}, CurrentVersion: "1.0.0", Bundles: []skillsync.Bundle{b}}
}
func execute(t *testing.T, cmdArgs ...string) (string, string, error) {
	t.Helper()
	cmd := New(commandConfig(t), CommandOptions{})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(cmdArgs)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func TestNewJSONUsesOnlyJSONOnStdout(t *testing.T) {
	dir := t.TempDir()
	out, errOut, err := execute(t, "sync", "--dir", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q", errOut)
	}
	var report skillsync.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not JSON: %q: %v", out, err)
	}
	if report.Dir != dir {
		t.Fatalf("dir = %q", report.Dir)
	}
}
func TestNewRejectsDirWithHarnessBeforeInstalling(t *testing.T) {
	dir := t.TempDir()
	_, _, err := execute(t, "sync", "--dir", dir, "--harness", "codex")
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, statErr := filepath.Glob(filepath.Join(dir, "*")); statErr != nil {
		t.Fatal(statErr)
	}
}
func TestNewDryRunDoesNotCreateTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	out, _, err := execute(t, "sync", "--dir", dir, "--dry-run", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report skillsync.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) == 0 {
		t.Fatal("no planned changes")
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run target stat = %v", err)
	}
}

func TestNewRejectsUnknownFormat(t *testing.T) {
	_, _, err := execute(t, "sync", "--dir", t.TempDir(), "--format", "yaml")
	if err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestNewSyncRetainsHostSiblingsAndMapsCobraInput(t *testing.T) {
	leaf := NewSync(commandConfig(t), CommandOptions{Errors: testMapper{}})
	if leaf.Name() != "sync" {
		t.Fatalf("leaf = %q", leaf.Name())
	}
	root := &cobra.Command{Use: "skills"}
	root.AddCommand(&cobra.Command{Use: "hook"}, leaf)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"sync", "unexpected"})
	err := root.Execute()
	var usage *UsageError
	if !errors.As(err, &usage) || !errors.As(err, new(mappedError)) {
		t.Fatalf("error = %v", err)
	}
	root.SetArgs([]string{"sync", "--unknown"})
	err = root.Execute()
	if !errors.As(err, &usage) || !errors.As(err, new(mappedError)) {
		t.Fatalf("unknown flag = %v", err)
	}
}

func TestDirBypassesHomeAndUsagePrecedesPreparation(t *testing.T) {
	dir := t.TempDir()
	opts := CommandOptions{Home: func() (string, error) { t.Fatal("--dir must bypass home"); return "", nil }, Errors: testMapper{}}
	_, _, err := executeWith(t, commandConfig(t), opts, context.Background(), "sync", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executeWith(t, commandConfig(t), opts, context.Background(), "sync", "--dir", dir, "--harness", "codex")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveTargetsPreservesSelectionOrderAndDeduplicatesPhysicalDirectory(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	harnesses := []Harness{
		{ID: "first", Aliases: []string{"one"}, ConfigRel: "first"},
		{ID: "second", ConfigRel: "second", ConfigEnv: "SECOND_HOME"},
	}
	targets, err := resolveTargets("", []string{"second,one", "second"}, harnesses, func() (string, error) { return home, nil }, func(key string) string {
		if key == "SECOND_HOME" {
			return shared
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Harness != "second" || targets[1].Harness != "first" {
		t.Fatalf("targets = %#v", targets)
	}
	all, err := resolveTargets("", []string{"all"}, harnesses, func() (string, error) { return home, nil }, func(key string) string {
		if key == "SECOND_HOME" {
			return shared
		}
		return ""
	})
	if err != nil || len(all) != 2 || all[0].Harness != "first" || all[1].Harness != "second" {
		t.Fatalf("all = %#v, %v", all, err)
	}
}

func TestPartialSuccessJSONAndConflictRemainFailureWithoutMapper(t *testing.T) {
	home := t.TempDir()
	bad := filepath.Join(home, "bad")
	good := filepath.Join(home, "good")
	if err := os.MkdirAll(filepath.Join(bad, "skills", "tool-install"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "skills", "tool-install", "SKILL.md"), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := CommandOptions{Harnesses: []Harness{{ID: "bad", ConfigRel: "bad"}, {ID: "good", ConfigRel: "good"}}, Home: func() (string, error) { return home, nil }}
	out, _, err := executeWith(t, commandConfig(t), opts, context.Background(), "sync", "--harness", "bad,good", "--format=json")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v", err)
	}
	var payload struct {
		Targets []struct {
			Harness string             `json:"harness"`
			Error   string             `json:"error"`
			Changes []skillsync.Change `json:"changes"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Targets) != 2 || payload.Targets[0].Harness != "bad" || payload.Targets[1].Harness != "good" {
		t.Fatalf("payload = %s", out)
	}
	if _, err := os.Stat(filepath.Join(good, "skills", "tool-install", "SKILL.md")); err != nil {
		t.Fatalf("independent success missing: %v", err)
	}
}

func TestRuntimeTargetFailureStillRendersIndependentSuccess(t *testing.T) {
	home := t.TempDir()
	broken := filepath.Join(home, "broken", "skills")
	good := filepath.Join(home, "good")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, skillsync.StateFileName), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := CommandOptions{Harnesses: []Harness{{ID: "broken", ConfigRel: "broken"}, {ID: "good", ConfigRel: "good"}}, Home: func() (string, error) { return home, nil }}
	out, _, err := executeWith(t, commandConfig(t), opts, context.Background(), "sync", "--harness", "broken,good", "--format=json")
	var aggregate *AggregateError
	if !errors.As(err, &aggregate) || len(aggregate.Results) != 2 {
		t.Fatalf("error = %v", err)
	}
	if !bytes.Contains([]byte(out), []byte(`"error"`)) {
		t.Fatalf("missing target error: %s", out)
	}
	if _, err := os.Stat(filepath.Join(good, "skills", "tool-install", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledCommandRendersNoTargetAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := executeWith(t, commandConfig(t), CommandOptions{}, ctx, "sync", "--dir", t.TempDir(), "--format=json")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestCustomRendererAndWriterFailuresUseHostFailure(t *testing.T) {
	called := false
	renderer := func(w io.Writer, results []TargetResult, format string) error {
		called = true
		_, err := io.WriteString(w, `{"legacy":true}`)
		return err
	}
	out, _, err := executeWith(t, commandConfig(t), CommandOptions{Renderer: renderer}, context.Background(), "sync", "--dir", t.TempDir(), "--format=json")
	if err != nil || !called || out != `{"legacy":true}` {
		t.Fatalf("out=%q called=%v err=%v", out, called, err)
	}
	cmd := New(commandConfig(t), CommandOptions{Errors: testMapper{}})
	cmd.SetOut(errWriter{})
	cmd.SetArgs([]string{"sync", "--dir", t.TempDir(), "--format=json"})
	err = cmd.Execute()
	if !errors.As(err, new(mappedError)) {
		t.Fatalf("writer error = %v", err)
	}
}

func TestTargetSelectionValidationAndDefaultDiscovery(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	targets, err := resolveTargets("", nil, DefaultHarnesses, func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil || len(targets) != 1 || targets[0].Harness != "cursor" {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	if !DefaultHarnesses[1].Present(home, func(string) string { return "" }) || DefaultHarnesses[0].Present(home, func(string) string { return "" }) {
		t.Fatal("present detection")
	}
	for _, args := range [][]string{{"unknown"}, {""}} {
		_, err := resolveTargets("", args, DefaultHarnesses, func() (string, error) { return home, nil }, func(string) string { return "" })
		if err == nil {
			t.Fatalf("want invalid targets for %q", args)
		}
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTargets(file, nil, nil, func() (string, error) { return home, nil }, func(string) string { return "" }); err == nil {
		t.Fatal("file target accepted")
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(filepath.Join(home, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTargets(link, nil, nil, func() (string, error) { return home, nil }, func(string) string { return "" }); err == nil {
		t.Fatal("symlink target accepted")
	}
	_, err = resolveTargets("", nil, nil, func() (string, error) { return "", errors.New("home unavailable") }, func(string) string { return "" })
	var usage *UsageError
	if errors.As(err, &usage) {
		t.Fatalf("runtime home error became usage: %v", err)
	}
}

func TestSameExistingTargetDeduplicatesAndAggregatePreservesErrors(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared")
	if err := os.MkdirAll(filepath.Join(shared, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	harnesses := []Harness{{ID: "one", ConfigRel: "one", ConfigEnv: "ONE"}, {ID: "two", ConfigRel: "two", ConfigEnv: "TWO"}}
	targets, err := resolveTargets("", []string{"one,two"}, harnesses, func() (string, error) { return home, nil }, func(string) string { return shared })
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	sentinel := errors.New("sentinel")
	aggregate := &AggregateError{Results: []TargetResult{{Err: sentinel}}}
	if aggregate.Error() == "" || !errors.Is(aggregate, sentinel) {
		t.Fatal("aggregate did not preserve error")
	}
	if (&UsageError{Err: sentinel}).Error() == "" || (&ConflictError{}).Error() == "" {
		t.Fatal("typed errors lack text")
	}
	if err := resultsError(CommandOptions{Errors: testMapper{}}, []TargetResult{{Report: skillsync.Report{Changes: []skillsync.Change{{Action: skillsync.Conflict}}}}}); !errors.As(err, new(mappedError)) {
		t.Fatalf("mapped conflict=%v", err)
	}
}

func TestPreparedSyncTargetsStopsOnCanceledContext(t *testing.T) {
	prepared, err := skillsync.Prepare(context.Background(), commandConfig(t), skillsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := syncTargets(ctx, prepared, []target{{Dir: t.TempDir()}}, false)
	if len(results) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("results=%#v err=%v", results, err)
	}
}

func TestPreparationErrorsAreMappedBeforeWritesAndNameConfiguredPlugin(t *testing.T) {
	dir := t.TempDir()
	resolver := &failingResolver{err: errors.New("resolver unavailable")}
	_, _, err := executeWith(t, commandConfig(t), CommandOptions{Resolver: resolver, Errors: testMapper{}}, context.Background(), "sync", "--dir", dir, "--newer-compatible")
	if resolver.calls != 1 || !errors.As(err, new(mappedError)) || !strings.Contains(err.Error(), "strongo/tool-plugin") {
		t.Fatalf("calls=%d error=%v", resolver.calls, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "tool-install")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("resolver failure wrote target: %v", statErr)
	}
	if getenvFunc(CommandOptions{})("definitely-absent-cli-helpers-env") != "" {
		t.Fatal("default getenv unexpected")
	}
	if _, err := resolveTargets("", nil, nil, func() (string, error) { return t.TempDir(), nil }, func(string) string { return "" }); err == nil {
		t.Fatal("empty configured harnesses accepted")
	}
	if _, err := normalizedTarget("\x00"); err == nil {
		t.Fatal("invalid path accepted")
	}
	if getenvFunc(CommandOptions{Getenv: func(string) string { return "configured" }})("x") != "configured" {
		t.Fatal("configured getenv lost")
	}
	if _, err := resolveTargets("", []string{"all"}, []Harness{{}}, func() (string, error) { return t.TempDir(), nil }, func(string) string { return "" }); err == nil {
		t.Fatal("empty harness ID accepted")
	}
	fallback, err := resolveTargets("", nil, []Harness{{ID: "only", ConfigRel: ".only"}}, func() (string, error) { return t.TempDir(), nil }, func(string) string { return "" })
	if err != nil || len(fallback) != 1 {
		t.Fatalf("fallback=%#v err=%v", fallback, err)
	}
	badRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTargets("", []string{"bad"}, []Harness{{ID: "bad", ConfigRel: "bad", ConfigEnv: "BAD_ROOT"}}, func() (string, error) { return t.TempDir(), nil }, func(string) string { return badRoot }); err == nil {
		t.Fatal("unsafe selected harness target accepted")
	}
	previousAbs := absolutePath
	absolutePath = func(string) (string, error) { return "", errors.New("abs failed") }
	t.Cleanup(func() { absolutePath = previousAbs })
	if _, err := normalizedTarget("relative"); err == nil {
		t.Fatal("absolute path failure accepted")
	}
}

func TestCanceledTargetRendersPartialResultsBeforeMappedFailure(t *testing.T) {
	previous := syncPrepared
	defer func() { syncPrepared = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	called := 0
	syncPrepared = func(_ context.Context, _ skillsync.Prepared, opts skillsync.Options) (skillsync.Report, error) {
		called++
		if called == 1 {
			cancel()
			return skillsync.Report{Dir: opts.Dir}, context.Canceled
		}
		return skillsync.Report{Dir: opts.Dir}, nil
	}
	home := t.TempDir()
	opts := CommandOptions{Harnesses: []Harness{{ID: "one", ConfigRel: "one"}, {ID: "two", ConfigRel: "two"}}, Home: func() (string, error) { return home, nil }, Errors: testMapper{}}
	out, _, err := executeWith(t, commandConfig(t), opts, ctx, "sync", "--harness=one,two", "--format=json")
	if called != 1 || !errors.Is(err, context.Canceled) || !bytes.Contains([]byte(out), []byte(`"one"`)) {
		t.Fatalf("called=%d err=%v out=%s", called, err, out)
	}
}
