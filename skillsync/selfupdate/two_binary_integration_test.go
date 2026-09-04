package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSelfUpdateRefreshesTheNewBinaryBundle(t *testing.T) {
	oldBinary := buildFixtureBinary(t, "1.0.0", "Embedded only in binary 1.\n")
	newBinary := buildFixtureBinary(t, "2.0.0", "Embedded only in binary 2.\n")
	server := newFixtureReleaseServer(t, newBinary)

	t.Run("check", func(t *testing.T) {
		runNoRefreshScenario(t, oldBinary, server, []string{"self-update", "--check", "--format", "json"}, false, false)
	})
	t.Run("dry-run", func(t *testing.T) {
		runNoRefreshScenario(t, oldBinary, server, []string{"self-update", "--dry-run", "--yes", "--format", "json"}, false, false)
	})
	t.Run("decline", func(t *testing.T) {
		runNoRefreshScenario(t, oldBinary, server, []string{"self-update", "--format", "json"}, true, false)
	})
	t.Run("checksum-failure", func(t *testing.T) {
		runNoRefreshScenario(t, oldBinary, server, []string{"self-update", "--yes", "--format", "json"}, false, true)
	})
	t.Run("successful-update", func(t *testing.T) {
		scenario := newFixtureScenario(t, oldBinary, server.URL)
		result := scenario.run(t, []string{"self-update", "--yes", "--format", "json"}, false)
		if result.exitCode != 0 {
			t.Fatalf("self-update failed: stderr=%s", result.stderr)
		}
		assertFixtureVersion(t, scenario.binary, "2.0.0", scenario.env)
		after := scenario.skill(t)
		if bytes.Equal(after, scenario.beforeSkill) || !bytes.Contains(after, []byte("binary 2")) {
			t.Fatalf("skills were not refreshed from the new binary: %q", after)
		}
		if got := scenario.refreshLog(t); got != "2.0.0\n" {
			t.Fatalf("refresh log = %q, want new binary", got)
		}
		var outcome map[string]any
		if err := json.Unmarshal(result.stdout, &outcome); err != nil {
			t.Fatalf("update stdout is not JSON: %v; stdout=%s", err, result.stdout)
		}
		if outcome["action"] != "updated" || outcome["after_update_warning"] != nil {
			t.Fatalf("outcome = %#v, want updated without warning", outcome)
		}
	})
	t.Run("refresh-failure-keeps-update-distinct", func(t *testing.T) {
		scenario := newFixtureScenario(t, oldBinary, server.URL)
		blocked := filepath.Join(scenario.dir, "not-a-directory")
		if err := os.WriteFile(blocked, []byte("preserve me"), 0o600); err != nil {
			t.Fatal(err)
		}
		scenario.env = setEnvironment(scenario.env, "FIXTURE_SKILLS_DIR", blocked)
		result := scenario.run(t, []string{"self-update", "--yes", "--format", "json"}, false)
		if result.exitCode != 0 {
			t.Fatalf("successful binary update became an error: stderr=%s", result.stderr)
		}
		assertFixtureVersion(t, scenario.binary, "2.0.0", scenario.env)
		if after := scenario.skill(t); !bytes.Equal(after, scenario.beforeSkill) {
			t.Fatalf("failed refresh changed prior skills: before=%q after=%q", scenario.beforeSkill, after)
		}
		if got := scenario.refreshLog(t); got != "2.0.0\n" {
			t.Fatalf("refresh log = %q, want attempted new binary", got)
		}
		var outcome map[string]any
		if err := json.Unmarshal(result.stdout, &outcome); err != nil {
			t.Fatalf("update stdout is not JSON: %v; stdout=%s", err, result.stdout)
		}
		warning, ok := outcome["after_update_warning"].(string)
		if !ok || !strings.Contains(warning, "retry with direct execution") {
			t.Fatalf("outcome warning = %#v, want direct retry", outcome["after_update_warning"])
		}
		if outcome["action"] != "updated" {
			t.Fatalf("outcome action = %#v, want updated", outcome["action"])
		}
	})
}

func runNoRefreshScenario(t *testing.T, oldBinary string, server *fixtureReleaseServer, args []string, interactive, corruptChecksum bool) {
	t.Helper()
	server.corruptChecksum = corruptChecksum
	t.Cleanup(func() { server.corruptChecksum = false })
	scenario := newFixtureScenario(t, oldBinary, server.URL)
	result := scenario.run(t, args, interactive)
	if corruptChecksum {
		if result.exitCode == 0 || !strings.Contains(string(result.stderr), "checksum") {
			t.Fatalf("checksum failure: exit=%d stderr=%s", result.exitCode, result.stderr)
		}
	} else if result.exitCode != 0 {
		t.Fatalf("read-only update path failed: stderr=%s", result.stderr)
	}
	assertFixtureVersion(t, scenario.binary, "1.0.0", scenario.env)
	if after := scenario.skill(t); !bytes.Equal(after, scenario.beforeSkill) {
		t.Fatalf("read-only update path changed skills: before=%q after=%q", scenario.beforeSkill, after)
	}
	if after, err := os.ReadFile(scenario.binary); err != nil || !bytes.Equal(after, scenario.beforeBinary) {
		t.Fatalf("read-only update path changed binary: err=%v", err)
	}
	if got := scenario.refreshLog(t); got != "" {
		t.Fatalf("read-only update path started refresh: %q", got)
	}
}

type fixtureScenario struct {
	dir            string
	binary         string
	skills         string
	refreshLogPath string
	env            []string
	beforeSkill    []byte
	beforeBinary   []byte
}

func newFixtureScenario(t *testing.T, oldBinary, endpoint string) *fixtureScenario {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "bin", fixtureBinaryName())
	copyFile(t, oldBinary, binary)
	skills := filepath.Join(dir, "skills")
	refreshLog := filepath.Join(dir, "refresh.log")
	env := setEnvironment(os.Environ(), "FIXTURE_RELEASE_ENDPOINT", endpoint)
	env = setEnvironment(env, "FIXTURE_SKILLS_DIR", skills)
	env = setEnvironment(env, "FIXTURE_REFRESH_LOG", refreshLog)
	env = setEnvironment(env, "FIXTURE_INTERACTIVE", "0")
	result := runFixture(t, binary, env, []string{"skills", "sync", "--dir", skills, "--format", "json"}, false)
	if result.exitCode != 0 {
		t.Fatalf("initial skills sync failed: stderr=%s", result.stderr)
	}
	if err := os.Remove(refreshLog); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	beforeSkill, err := os.ReadFile(filepath.Join(skills, "fixture-command", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	beforeBinary, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureScenario{dir: dir, binary: binary, skills: skills, refreshLogPath: refreshLog, env: env, beforeSkill: beforeSkill, beforeBinary: beforeBinary}
}

func (s *fixtureScenario) run(t *testing.T, args []string, interactive bool) fixtureResult {
	t.Helper()
	env := setEnvironment(s.env, "FIXTURE_INTERACTIVE", map[bool]string{true: "1", false: "0"}[interactive])
	return runFixture(t, s.binary, env, args, interactive)
}

func (s *fixtureScenario) skill(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(s.skills, "fixture-command", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func (s *fixtureScenario) refreshLog(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(s.refreshLogPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type fixtureResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

func runFixture(t *testing.T, binary string, env, args []string, interactive bool) fixtureResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	if interactive {
		cmd.Stdin = strings.NewReader("n\n")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("fixture command timed out: %v", ctx.Err())
	}
	result := fixtureResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run fixture: %v", err)
	}
	result.exitCode = exitErr.ExitCode()
	return result
}

func assertFixtureVersion(t *testing.T, binary, want string, env []string) {
	t.Helper()
	result := runFixture(t, binary, env, []string{"--version"}, false)
	if result.exitCode != 0 || strings.TrimSpace(string(result.stdout)) != want {
		t.Fatalf("version: exit=%d stdout=%q stderr=%q; want %q", result.exitCode, result.stdout, result.stderr, want)
	}
}

type fixtureReleaseServer struct {
	URL             string
	corruptChecksum bool
	asset           string
	archive         []byte
	checksum        string
}

func newFixtureReleaseServer(t *testing.T, binary string) *fixtureReleaseServer {
	t.Helper()
	archive := fixtureArchive(t, binary)
	asset := "fixture-cli_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + fixtureArchiveExtension()
	server := &fixtureReleaseServer{asset: asset, archive: archive, checksum: sha256Text(archive)}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			_, _ = io.WriteString(w, `[{"tag_name":"v2.0.0","draft":false,"prerelease":false}]`)
		case "/assets/fixture-cli_2.0.0_checksums.txt":
			checksum := server.checksum
			if server.corruptChecksum {
				checksum = strings.Repeat("0", 64)
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksum, server.asset)
		case "/assets/" + server.asset:
			_, _ = w.Write(server.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	server.URL = httpServer.URL
	t.Cleanup(httpServer.Close)
	return server
}

func buildFixtureBinary(t *testing.T, version, skillText string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixture")
	copyFixtureTree(t, filepath.Join("testdata", "two_binary"), dir)
	if err := os.WriteFile(filepath.Join(dir, "skills", "fixture-command", "SKILL.md"), []byte("---\nname: fixture-command\ndescription: Fixture binary "+version+" instructions.\n---\n\n"+skillText), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module := "module example.invalid/fixture\n\ngo 1.26.0\n\ntoolchain go1.27.0\n\nrequire github.com/strongo/cli-helpers v0.0.0\n\nreplace github.com/strongo/cli-helpers => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, fixtureBinaryName())
	cmd := exec.Command("go", "build", "-mod=mod", "-ldflags", "-X main.buildVersion="+version, "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = setEnvironment(os.Environ(), "GOWORK", "off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fixture binary %s: %v\n%s", version, err, output)
	}
	return binary
}

func copyFixtureTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func fixtureArchive(t *testing.T, binary string) []byte {
	t.Helper()
	content, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if runtime.GOOS == "windows" {
		writer := zip.NewWriter(&output)
		header := &zip.FileHeader{Name: fixtureBinaryName(), Method: zip.Deflate}
		header.SetMode(0o755)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: fixtureBinaryName(), Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func fixtureBinaryName() string {
	if runtime.GOOS == "windows" {
		return "fixture-cli.exe"
	}
	return "fixture-cli"
}

func fixtureArchiveExtension() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func sha256Text(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func setEnvironment(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
