package cliinstall

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestMain installs a guard over http.DefaultTransport for the whole
// package's test run (cli-install#req:no-network-in-tests — task-5 review
// S5: "add a guard test helper that fails tests on real dial"). A test that
// forgets to set selfupdate.Config.HTTPClient (or ConfigureRelease
// altogether) falls back to http.DefaultClient, which uses
// http.DefaultTransport — exactly the path that let a real dial to
// api.github.com slip through undetected before. Every httptest.Server
// fixture in this package's own tests dials 127.0.0.1 through its own
// srv.Client(), which carries its own *http.Transport and is never routed
// through this guard, so legitimate offline tests are unaffected.
func TestMain(m *testing.M) {
	http.DefaultTransport = guardedTransport{base: http.DefaultTransport}
	os.Exit(m.Run())
}

// guardedTransport refuses any request whose host is not a loopback
// address, with a message naming exactly what went wrong, and otherwise
// delegates to base — the REAL default transport captured before this
// package replaced http.DefaultTransport, never the (now-guarded) global
// itself, which would recurse forever.
type guardedTransport struct{ base http.RoundTripper }

func (g guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("cli-install test attempted a real network request to %s; inject a local httptest.Server instead (cli-install#req:no-network-in-tests)", req.URL)
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
