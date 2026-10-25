package cliui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestUninstallRowName(t *testing.T) {
	row1 := UninstallRow{
		Entry: cliinstall.Entry{ID: "specscore"},
		Result: cliinstall.UninstallResult{
			Target: "specscore-target",
		},
	}
	if name := UninstallRowName(row1); name != "specscore" {
		t.Errorf("UninstallRowName(row1) = %q, want specscore", name)
	}

	row2 := UninstallRow{
		Result: cliinstall.UninstallResult{
			Target: "custom-target",
		},
	}
	if name := UninstallRowName(row2); name != "custom-target" {
		t.Errorf("UninstallRowName(row2) = %q, want custom-target", name)
	}
}

func TestWriteUninstallReport(t *testing.T) {
	var out, errOut bytes.Buffer

	// 1. Empty rows
	if err := WriteUninstallReport(&out, &errOut, "wb", nil, false); err != nil {
		t.Fatalf("WriteUninstallReport failed: %v", err)
	}
	if !strings.Contains(out.String(), "No matching installed tools to uninstall.") {
		t.Errorf("output = %q, want 'No matching installed tools to uninstall.'", out.String())
	}

	// 2. All outcomes and branches
	out.Reset()
	errOut.Reset()

	rows := []UninstallRow{
		{
			Entry: cliinstall.Entry{ID: "tool-direct"},
			Result: cliinstall.UninstallResult{
				Outcome:  cliinstall.UninstallOutcomeUninstalled,
				Method:   cliinstall.UninstallMethodDirect,
				Path:     "/usr/local/bin/tool-direct",
				Warnings: []string{"warning direct"},
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-brew", CaskToken: "tool-brew"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeUninstalled,
				Method:  cliinstall.UninstallMethodHomebrew,
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-dry-direct"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeDryRun,
				Method:  cliinstall.UninstallMethodDirect,
				Path:    "/usr/local/bin/tool-dry-direct",
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-dry-brew"},
			Result: cliinstall.UninstallResult{
				Outcome:  cliinstall.UninstallOutcomeDryRun,
				Method:   cliinstall.UninstallMethodHomebrew,
				CaskArgv: []string{"brew", "uninstall", "--cask", "tool-dry-brew"},
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-not-installed"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeNotInstalled,
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-redirected"},
			Result: cliinstall.UninstallResult{
				Outcome:  cliinstall.UninstallOutcomeRedirected,
				CaskArgv: []string{"brew", "uninstall", "--cask", "tool-redirected"},
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-failed-err"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeFailed,
				Failure: &selfupdate.Failure{
					Err: errors.New("permission denied"),
				},
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-failed-nil"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeFailed,
			},
		},
		{
			Entry: cliinstall.Entry{ID: "tool-unknown"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcome(999),
			},
		},
	}

	if err := WriteUninstallReport(&out, &errOut, "wb", rows, false); err != nil {
		t.Fatalf("WriteUninstallReport failed: %v", err)
	}

	outStr := out.String()
	for _, expected := range []string{
		"✓ Uninstalled tool-direct (/usr/local/bin/tool-direct)",
		"✓ Uninstalled tool-brew (via brew cask tool-brew)",
		"• Would remove tool-dry-direct (/usr/local/bin/tool-dry-direct)",
		"• Would uninstall tool-dry-brew via brew uninstall --cask tool-dry-brew",
		"- tool-not-installed is not installed",
		"! tool-redirected: to uninstall, run: brew uninstall --cask tool-redirected",
		"✗ Failed to uninstall tool-failed-err: permission denied",
		"✗ Failed to uninstall tool-failed-nil",
		"? tool-unknown: unknown",
	} {
		if !strings.Contains(outStr, expected) {
			t.Errorf("output missing %q; full output:\n%s", expected, outStr)
		}
	}

	if !strings.Contains(errOut.String(), "warning (tool-direct): warning direct") {
		t.Errorf("errOut missing warning: %q", errOut.String())
	}
}

func TestWriteUninstallJSON(t *testing.T) {
	var out bytes.Buffer

	rows := []UninstallRow{
		{
			Entry: cliinstall.Entry{ID: "specscore"},
			Result: cliinstall.UninstallResult{
				Outcome:  cliinstall.UninstallOutcomeUninstalled,
				Method:   cliinstall.UninstallMethodDirect,
				Path:     "/usr/local/bin/specscore",
				Warnings: []string{"some warning"},
			},
		},
		{
			Entry: cliinstall.Entry{ID: "failed-tool"},
			Result: cliinstall.UninstallResult{
				Outcome: cliinstall.UninstallOutcomeFailed,
				Method:  cliinstall.UninstallMethodHomebrew,
				Failure: &selfupdate.Failure{
					Err: errors.New("command failed"),
				},
			},
		},
	}

	if err := WriteUninstallJSON(&out, "wb", rows); err != nil {
		t.Fatalf("WriteUninstallJSON failed: %v", err)
	}

	jsonStr := out.String()
	if !strings.Contains(jsonStr, `"host": "wb"`) ||
		!strings.Contains(jsonStr, `"name": "specscore"`) ||
		!strings.Contains(jsonStr, `"outcome": "uninstalled"`) ||
		!strings.Contains(jsonStr, `"name": "failed-tool"`) ||
		!strings.Contains(jsonStr, `"error": "command failed"`) {
		t.Errorf("unexpected json output:\n%s", jsonStr)
	}
}
