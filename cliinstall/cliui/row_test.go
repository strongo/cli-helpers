package cliui

import (
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
)

func TestRowName_FromEntry(t *testing.T) {
	row := Row{Entry: cliinstall.Entry{ID: "ovdb"}}
	if got := RowName(row); got != "ovdb" {
		t.Errorf("RowName = %q, want ovdb", got)
	}
}

func TestRowName_FromPlanWhenEntryUnknown(t *testing.T) {
	row := Row{Plan: &cliinstall.Result{Target: "nosuchcli"}}
	if got := RowName(row); got != "nosuchcli" {
		t.Errorf("RowName = %q, want nosuchcli", got)
	}
}

func TestRowName_FromStatusAsLastResort(t *testing.T) {
	row := Row{Status: cliinstall.Status{ID: "fallback"}}
	if got := RowName(row); got != "fallback" {
		t.Errorf("RowName = %q, want fallback", got)
	}
}
