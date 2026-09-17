package daemonlifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The probe is this test binary re-executed with probeModeEnv set. It stands in
// for a CLI daemon: "start" launches a detached "serve" child, waits for its
// whoami, prints one line and exits; "serve" answers whoami and shuts down only
// for the secret it wrote; "whoami" is a fresh client; "sleep" just runs.
const (
	probeModeEnv = "DAEMONLIFECYCLE_PROBE"
	probeDirEnv  = "DAEMONLIFECYCLE_PROBE_DIR"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(probeModeEnv); mode != "" {
		if err := runProbe(mode, os.Getenv(probeDirEnv)); err != nil {
			fmt.Fprintln(os.Stderr, "probe:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func probeCommand(mode, dir string) *exec.Cmd {
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), probeModeEnv+"="+mode, probeDirEnv+"="+dir)
	return command
}

func runProbe(mode, dir string) error {
	switch mode {
	case "serve":
		return probeServe(dir)
	case "start":
		return probeStart(dir)
	case "whoami":
		pid, err := probeWhoami(dir)
		if err == nil {
			fmt.Println(pid)
		}
		return err
	case "sleep":
		time.Sleep(time.Minute)
		return nil
	}
	return runPlatformProbe(mode, dir)
}

func probeServe(dir string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	if err := writeProbeFile(dir, "secret", token); err != nil {
		return err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, os.Getpid())
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		go func() { _ = server.Shutdown(context.Background()) }()
	})
	server.Handler = mux
	port := listener.Addr().(*net.TCPAddr).Port
	if err := writeProbeFile(dir, "port", strconv.Itoa(port)); err != nil {
		return err
	}
	fmt.Println("serving on", port)
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// writeProbeFile publishes a complete owner-only file with one rename.
func writeProbeFile(dir, name, value string) error {
	staged := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(staged, []byte(value), 0o600); err != nil {
		return err
	}
	if err := ProtectOwnerOnly(staged); err != nil {
		return err
	}
	return os.Rename(staged, filepath.Join(dir, name))
}

func probeStart(dir string) error {
	log, err := os.OpenFile(filepath.Join(dir, "server.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	process, err := StartDetached(probeCommand("serve", dir), log)
	_ = log.Close()
	if err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		_, _ = process.Wait()
		close(exited)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("server exited before readiness")
		default:
		}
		if pid, err := probeWhoami(dir); err == nil && pid == process.Pid {
			fmt.Printf("ready %d\n", pid)
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = process.Kill()
	return errors.New("server not ready within 10s")
}

func probePort(dir string) (string, error) {
	port, err := os.ReadFile(filepath.Join(dir, "port"))
	return string(port), err
}

func probeWhoami(dir string) (int, error) {
	port, err := probePort(dir)
	if err != nil {
		return 0, err
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Get("http://127.0.0.1:" + port + "/whoami")
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(body))
}

// waitFor polls condition every 20 ms for up to 10 s.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:detached-start-journey
func TestStartDetachedReturnsToPipedCaller(t *testing.T) {
	dir := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	start := probeCommand("start", dir)
	start.Stdout = writer
	var stderr bytes.Buffer
	start.Stderr = &stderr
	if err := start.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()

	line, err := readLine(reader)
	readyAt := time.Now()
	if err != nil {
		_ = start.Wait()
		t.Fatalf("read readiness: %v; probe stderr: %s", err, stderr.String())
	}
	var pid int
	if _, err := fmt.Sscanf(line, "ready %d", &pid); err != nil {
		t.Fatalf("readiness line %q: %v", line, err)
	}
	started, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = TerminateIfSameProcess(pid, started) })
	assertNoInheritedPipe(t, pid, reader)

	// The reader sees EOF although the server keeps running: nothing but the
	// exiting start process ever held the pipe's write end.
	eof := make(chan error, 1)
	go func() {
		rest, err := io.ReadAll(reader)
		if err == nil && len(rest) != 0 {
			err = fmt.Errorf("unexpected output after readiness: %q", rest)
		}
		eof <- err
	}()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2*time.Second - time.Since(readyAt)):
		t.Fatal("no EOF within 2s of readiness: the detached child holds the caller's pipe")
	}
	if err := start.Wait(); err != nil {
		t.Fatalf("start probe: %v; stderr: %s", err, stderr.String())
	}

	// A fresh process finds the detached server.
	output, err := probeCommand("whoami", dir).Output()
	if err != nil {
		t.Fatalf("fresh whoami: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != strconv.Itoa(pid) {
		t.Fatalf("fresh whoami = %q, want %d", got, pid)
	}

	port, err := probePort(dir)
	if err != nil {
		t.Fatal(err)
	}
	if code := probeShutdown(t, port, "wrong"); code != http.StatusForbidden {
		t.Fatalf("shutdown with a wrong secret = %d, want 403", code)
	}
	if again, err := probeWhoami(dir); err != nil || again != pid {
		t.Fatalf("after refused shutdown whoami = %d, %v", again, err)
	}
	secret, err := os.ReadFile(filepath.Join(dir, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if code := probeShutdown(t, port, string(secret)); code != http.StatusOK {
		t.Fatalf("authenticated shutdown = %d, want 200", code)
	}
	waitFor(t, "the port to be free", func() bool {
		listener, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			return false
		}
		_ = listener.Close()
		return true
	})
	waitFor(t, "the server to exit", func() bool {
		_, err := ProcessStartTime(pid)
		return errors.Is(err, ErrProcessNotFound)
	})
}

func readLine(reader io.Reader) (string, error) {
	var line []byte
	buffer := make([]byte, 1)
	for {
		if _, err := reader.Read(buffer); err != nil {
			return string(line), err
		}
		if buffer[0] == '\n' {
			return strings.TrimSpace(string(line)), nil
		}
		line = append(line, buffer[0])
	}
}

func probeShutdown(t *testing.T, port, secret string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:detached-start-journey
func TestStartDetachedRunsWithLogAndNewSession(t *testing.T) {
	dir := t.TempDir()
	log, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	template := probeCommand("serve", dir)
	template.Dir = dir
	process, err := StartDetached(template, log)
	if closeErr := log.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_, _ = process.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = process.Kill()
		<-exited
	})
	waitFor(t, "whoami", func() bool {
		pid, err := probeWhoami(dir)
		return err == nil && pid == process.Pid
	})
	assertDetachedSession(t, process.Pid)
	waitFor(t, "the log line", func() bool {
		contents, err := os.ReadFile(filepath.Join(dir, "server.log"))
		return err == nil && strings.Contains(string(contents), "serving on")
	})
}

func TestStartDetachedRejectsUnusableTemplates(t *testing.T) {
	log, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	valid := func() *exec.Cmd { return probeCommand("sleep", "") }
	started := valid()
	started.Process = &os.Process{Pid: 1}
	withStdout, withFiles, withAttr := valid(), valid(), valid()
	withStdout.Stdout = io.Discard
	withFiles.ExtraFiles = []*os.File{log}
	withAttr.SysProcAttr = new(syscall.SysProcAttr)
	for name, test := range map[string]struct {
		template *exec.Cmd
		log      *os.File
		want     string
	}{
		"nil command":  {nil, log, "nil command"},
		"nil log":      {valid(), nil, "nil log"},
		"lookup error": {exec.Command("daemonlifecycle-missing-command"), log, "not found"},
		"started":      {started, log, "already started"},
		"stdio":        {withStdout, log, "Stdout"},
		"extra files":  {withFiles, log, "ExtraFiles"},
		"sysprocattr":  {withAttr, log, "SysProcAttr"},
		"missing path": {&exec.Cmd{Path: filepath.Join(t.TempDir(), "missing")}, log, "start detached"},
	} {
		t.Run(name, func(t *testing.T) {
			if process, err := StartDetached(test.template, test.log); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StartDetached = %v, %v; want error containing %q", process, err, test.want)
			}
		})
	}
}

func TestStartDetachedRetriesOnlyAcceptedFailures(t *testing.T) {
	dir := t.TempDir()
	log, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	missing := filepath.Join(dir, "missing")
	breakMissing := func(command *exec.Cmd) { command.Path = missing }
	// Unix reports a missing executable as ENOENT, Windows as a failed lookup;
	// both name the path.
	isMissing := func(err error) bool { return err != nil && strings.Contains(err.Error(), missing) }
	retryMissing := isMissing

	process, err := startDetached(probeCommand("sleep", dir), log,
		[]func(*exec.Cmd){breakMissing, ConfigureDetached}, retryMissing)
	if err != nil {
		t.Fatalf("retry after an accepted failure: %v", err)
	}
	_ = process.Kill()
	_, _ = process.Wait()

	if _, err := startDetached(probeCommand("sleep", dir), log,
		[]func(*exec.Cmd){breakMissing, ConfigureDetached}, func(error) bool { return false }); !isMissing(err) {
		t.Fatalf("non-retryable failure = %v, want the first error", err)
	}
	if _, err := startDetached(probeCommand("sleep", dir), log,
		[]func(*exec.Cmd){breakMissing, breakMissing}, retryMissing); !isMissing(err) {
		t.Fatalf("exhausted retries = %v, want the last error", err)
	}
}

// specscore:verifies https://specscore.org/github.com/strongo/cli-helpers/spec/features/daemon-lifecycle#ac:reused-pid-never-signalled
func TestTerminateIfSameProcessNeverSignalsAnotherStartTime(t *testing.T) {
	sleeper := probeCommand("sleep", "")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- sleeper.Wait() }()
	pid := sleeper.Process.Pid
	started, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := ProcessStartTime(pid); err != nil || !again.Equal(started) {
		t.Fatalf("start time is unstable: %v then %v, %v", started, again, err)
	}
	if since := time.Since(started); since < -time.Minute || since > time.Minute {
		t.Fatalf("start time %v is not near now", started)
	}

	for _, recorded := range []time.Time{started.Add(-time.Second), started.Add(10 * time.Millisecond), {}} {
		if err := TerminateIfSameProcess(pid, recorded); !errors.Is(err, ErrProcessMismatch) {
			t.Fatalf("TerminateIfSameProcess(%v) = %v, want ErrProcessMismatch", recorded, err)
		}
	}
	select {
	case err := <-exited:
		t.Fatalf("a mismatched process was signalled: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := TerminateIfSameProcess(pid, started); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the matching process was not terminated")
	}
	if _, err := ProcessStartTime(pid); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("start time of a reaped process = %v, want ErrProcessNotFound", err)
	}
	if err := TerminateIfSameProcess(pid, started); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("terminate a reaped process = %v, want ErrProcessNotFound", err)
	}
}

func TestProcessFunctionsRejectInvalidPids(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, err := ProcessStartTime(pid); err == nil || errors.Is(err, ErrProcessNotFound) {
			t.Fatalf("ProcessStartTime(%d) = %v", pid, err)
		}
		if err := TerminateIfSameProcess(pid, time.Now()); err == nil || errors.Is(err, ErrProcessMismatch) {
			t.Fatalf("TerminateIfSameProcess(%d) = %v", pid, err)
		}
	}
	if _, err := ProcessStartTime(os.Getpid()); err != nil {
		t.Fatalf("own start time: %v", err)
	}
}
