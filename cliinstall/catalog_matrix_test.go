package cliinstall

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestMatrixStructuralRules proves the structural half of
// cli-install#req:catalog-validated: every relevance entry names a catalog
// id, no host lists itself or a target twice, every catalog CLI appears as
// a host, and no relevance text is duplicated.
func TestMatrixStructuralRules(t *testing.T) {
	ids := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		ids[id] = true
	}

	hostsSeen := map[string]bool{}
	seenPair := map[[2]string]bool{}
	seenText := map[string]string{} // text -> "host->target" that first used it

	for _, row := range matrixRows {
		if !ids[row.Host] {
			t.Errorf("row host %q is not a catalog id", row.Host)
		}
		if !ids[row.Target] {
			t.Errorf("row target %q is not a catalog id", row.Target)
		}
		if row.Host == row.Target {
			t.Errorf("row %q -> %q: a target must not be relevant to itself", row.Host, row.Target)
		}
		pair := [2]string{row.Host, row.Target}
		if seenPair[pair] {
			t.Errorf("host %q lists target %q twice", row.Host, row.Target)
		}
		seenPair[pair] = true
		hostsSeen[row.Host] = true

		if prior, dup := seenText[row.Text]; dup {
			t.Errorf("relevance text duplicated between %q and %q->%q: %q", prior, row.Host, row.Target, row.Text)
		}
		seenText[row.Text] = row.Host + "->" + row.Target
	}

	for id := range ids {
		if !hostsSeen[id] {
			t.Errorf("catalog id %q never appears as a matrix host", id)
		}
	}
}

// TestRelevantMatchesMatrixOrder proves Relevant filters matrixRows by host
// while preserving table order (cli-install#req:list-relevant).
func TestRelevantMatchesMatrixOrder(t *testing.T) {
	var wantWBTargets []string
	for _, row := range matrixRows {
		if row.Host == "wb" {
			wantWBTargets = append(wantWBTargets, row.Target)
		}
	}
	got := Relevant("wb")
	if len(got) != len(wantWBTargets) {
		t.Fatalf("Relevant(\"wb\") has %d rows, want %d", len(got), len(wantWBTargets))
	}
	for i, r := range got {
		if r.Target != wantWBTargets[i] {
			t.Errorf("Relevant(\"wb\")[%d].Target = %q, want %q", i, r.Target, wantWBTargets[i])
		}
	}
}

func TestRelevantUnknownHost(t *testing.T) {
	if got := Relevant("nosuchcli"); got != nil {
		t.Errorf("Relevant(\"nosuchcli\") = %v, want nil", got)
	}
}

// TestMatrixEqualsFeatureTable proves matrixRows equals
// spec/features/cli-install/README.md's REQ: relevance-matrix table
// (cli-install#req:catalog-validated: "the matrix equals the table above").
// It parses the actual spec file rather than a second hard-coded copy, so a
// spec edit that isn't mirrored in catalog_matrix.go — in either
// direction — fails this test.
func TestMatrixEqualsFeatureTable(t *testing.T) {
	rows := parseFeatureMatrixTable(t)
	if !reflect.DeepEqual(rows, matrixRows) {
		t.Fatalf("matrixRows does not equal spec/features/cli-install/README.md's table.\ngot  %d rows: %+v\nwant %d rows: %+v",
			len(matrixRows), matrixRows, len(rows), rows)
	}
}

// parseFeatureMatrixTable reads the REQ: relevance-matrix markdown table
// from the Feature spec and returns its rows in document order.
func parseFeatureMatrixTable(t *testing.T) []relevanceRow {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve this test file's path")
	}
	specPath := filepath.Join(filepath.Dir(thisFile), "..", "spec", "features", "cli-install", "README.md")
	f, err := os.Open(specPath)
	if err != nil {
		t.Fatalf("open %s: %v", specPath, err)
	}
	defer func() { _ = f.Close() }()

	var rows []relevanceRow
	inTable := false
	sawHeaderSeparator := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "#### REQ: relevance-matrix"):
			inTable = false
			sawHeaderSeparator = false
		case !inTable && strings.HasPrefix(line, "| Host → target |"):
			inTable = true
			continue
		case inTable && !sawHeaderSeparator && strings.HasPrefix(line, "|---"):
			sawHeaderSeparator = true
			continue
		case inTable && sawHeaderSeparator && strings.HasPrefix(line, "|"):
			row, ok := parseMatrixTableRow(line)
			if !ok {
				t.Fatalf("could not parse matrix table row: %q", line)
			}
			rows = append(rows, row)
		case inTable && sawHeaderSeparator:
			// First non-"|" line after the header ends the table.
			inTable = false
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", specPath, err)
	}
	if len(rows) == 0 {
		t.Fatalf("found no relevance-matrix rows in %s", specPath)
	}
	return rows
}

// parseMatrixTableRow parses one "| `host` → `target` | text | basis |"
// markdown row.
func parseMatrixTableRow(line string) (relevanceRow, bool) {
	parts := strings.Split(line, "|")
	// "| a | b | c |" splits into ["", " a ", " b ", " c ", ""].
	if len(parts) != 5 {
		return relevanceRow{}, false
	}
	hostTarget := strings.TrimSpace(parts[1])
	text := strings.TrimSpace(parts[2])

	arrow := strings.SplitN(hostTarget, "→", 2)
	if len(arrow) != 2 {
		return relevanceRow{}, false
	}
	host := strings.Trim(strings.TrimSpace(arrow[0]), "`")
	target := strings.Trim(strings.TrimSpace(arrow[1]), "`")
	if host == "" || target == "" || text == "" {
		return relevanceRow{}, false
	}
	return relevanceRow{Host: host, Target: target, Text: text}, true
}
