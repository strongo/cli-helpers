package cobracmd

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestMain installs a guard over http.DefaultTransport for the whole
// package's test run (cli-install#req:no-network-in-tests — task-5 review
// S5). See cliinstall/main_test.go's own doc comment for the full
// rationale: this package's tests build a real Cobra command and, through
// it, real cliinstall.Options/selfupdate.Config values — a test that
// forgets to set CommandOptions.ConfigureRelease falls back to the real
// GitHub API exactly as easily here as in cliinstall's own tests.
func TestMain(m *testing.M) {
	http.DefaultTransport = guardedTransport{base: http.DefaultTransport}
	os.Exit(m.Run())
}

type guardedTransport struct{ base http.RoundTripper }

func (g guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("cliinstall/cobracmd test attempted a real network request to %s; inject a local httptest.Server instead (cli-install#req:no-network-in-tests)", req.URL)
	}
	return g.base.RoundTrip(req)
}

func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
