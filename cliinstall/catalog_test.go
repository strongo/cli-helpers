package cliinstall

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// wantIDs is every catalog id this Feature's Consumers table names
// (spec/features/cli-install/README.md). It is transcribed independently of
// catalog.go's registration so a missing or extra entry fails a test
// instead of silently shrinking or growing the compiled-in catalog.
var wantIDs = []string{
	"chatwright", "codegrapher", "cover100", "datatug", "ingitdb",
	"ovdb", "specscore", "synchestra", "wb",
}

func TestIDs(t *testing.T) {
	got := IDs()
	if !sort.StringsAreSorted(got) {
		t.Errorf("IDs() = %v, not sorted", got)
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("IDs() = %v, want %v", got, wantIDs)
	}
	want := append([]string(nil), wantIDs...)
	sort.Strings(want)
	for i, id := range got {
		if id != want[i] {
			t.Errorf("IDs()[%d] = %q, want %q", i, id, want[i])
		}
	}
}

func TestEntries(t *testing.T) {
	entries := Entries()
	if len(entries) != len(wantIDs) {
		t.Fatalf("Entries() has %d entries, want %d", len(entries), len(wantIDs))
	}
	for _, e := range entries {
		if e.ID == "" {
			t.Errorf("entry with empty ID: %+v", e)
		}
		if e.Homepage == "" {
			t.Errorf("%s: empty Homepage", e.ID)
		}
		if e.Description == "" {
			t.Errorf("%s: empty Description", e.ID)
		}
		if e.Details == "" {
			t.Errorf("%s: empty Details", e.ID)
		}
		if e.Repository == "" {
			t.Errorf("%s: empty Repository", e.ID)
		}
		if len(e.SupportedPlatforms) == 0 {
			t.Errorf("%s: no SupportedPlatforms", e.ID)
		}
		if e.HasCask() && len(e.CaskOS) == 0 {
			t.Errorf("%s: has a CaskToken but no CaskOS", e.ID)
		}
		if !e.HasCask() && len(e.CaskOS) != 0 {
			t.Errorf("%s: no CaskToken but CaskOS is set", e.ID)
		}
	}
}

// Entries returns a defensive copy: mutating what it returns must never
// change the compiled-in catalog observed by a later call.
func TestEntriesReturnsCopy(t *testing.T) {
	first := Entries()
	first[0].ID = "mutated"
	second := Entries()
	if second[0].ID == "mutated" {
		t.Fatal("Entries() did not return a defensive copy")
	}
}

func TestByID(t *testing.T) {
	for _, id := range wantIDs {
		e, ok := ByID(id)
		if !ok {
			t.Errorf("ByID(%q) not found", id)
			continue
		}
		if e.ID != id {
			t.Errorf("ByID(%q).ID = %q", id, e.ID)
		}
	}
	if _, ok := ByID("nosuchcli"); ok {
		t.Error("ByID(\"nosuchcli\") = true, want false")
	}
}

func TestEntryConfig(t *testing.T) {
	for _, id := range wantIDs {
		e, _ := ByID(id)
		cfg := e.Config("1.2.3")
		if cfg.BinaryName != e.ID {
			t.Errorf("%s: Config().BinaryName = %q", id, cfg.BinaryName)
		}
		if cfg.Repository != e.Repository {
			t.Errorf("%s: Config().Repository = %q, want %q", id, cfg.Repository, e.Repository)
		}
		if cfg.CurrentVersion != "1.2.3" {
			t.Errorf("%s: Config().CurrentVersion = %q", id, cfg.CurrentVersion)
		}
		if cfg.TagPrefix != e.TagPrefix {
			t.Errorf("%s: Config().TagPrefix = %q, want %q", id, cfg.TagPrefix, e.TagPrefix)
		}
		if len(cfg.SupportedPlatforms) != len(e.SupportedPlatforms) {
			t.Errorf("%s: Config().SupportedPlatforms length mismatch", id)
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("register() with a duplicate id did not panic")
		}
	}()
	register(Entry{ID: "wb"})
}

func TestEntryHasCask(t *testing.T) {
	withCask, _ := ByID("wb")
	if !withCask.HasCask() {
		t.Error("wb: HasCask() = false, want true")
	}
	without, _ := ByID("cover100")
	if without.HasCask() {
		t.Error("cover100: HasCask() = true, want false")
	}
}

// TestNoCatalogIDInSelfupdateSource proves the selfupdate package (and its
// cobracmd/cliui subpackages) carries no catalog CLI's identity
// (cli-install#req:catalog-identity-single-source). It parses every
// non-test .go file's AST and fails on a string literal or identifier that
// exactly equals a catalog id — not a substring search, which would flag
// selfupdate's own doc comments and its test files' long-standing use of
// "wb"/"acme/wb" as a generic example binary name, neither of which is a
// per-CLI identity constructor.
func TestNoCatalogIDInSelfupdateSource(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve this test file's path")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "selfupdate")

	ids := IDs()
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(v.Value)
				if uerr != nil {
					return true
				}
				if containsID(ids, val) {
					t.Errorf("%s:%s: string literal %q equals catalog id", path, fset.Position(v.Pos()), val)
				}
			case *ast.Ident:
				if containsID(ids, v.Name) {
					t.Errorf("%s:%s: identifier %q equals catalog id", path, fset.Position(v.Pos()), v.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func containsID(ids []string, s string) bool {
	for _, id := range ids {
		if id == s {
			return true
		}
	}
	return false
}
