package selfupdate

import (
	"reflect"
	"testing"
)

func TestHomebrew(t *testing.T) {
	m := Homebrew("brew upgrade --cask wb")
	if m.Name != "Homebrew" {
		t.Errorf("Name = %q, want Homebrew", m.Name)
	}
	if m.UpgradeCommand != "brew upgrade --cask wb" {
		t.Errorf("UpgradeCommand = %q", m.UpgradeCommand)
	}
	want := []string{"/cellar/", "/homebrew/", "/linuxbrew/", "/caskroom/"}
	if !reflect.DeepEqual(m.PathMarkers, want) {
		t.Errorf("PathMarkers = %v, want %v", m.PathMarkers, want)
	}
}

func TestScoop(t *testing.T) {
	m := Scoop("scoop update specscore")
	if m.Name != "Scoop" {
		t.Errorf("Name = %q, want Scoop", m.Name)
	}
	want := []string{"/scoop/apps/", "/scoop/shims/"}
	if !reflect.DeepEqual(m.PathMarkers, want) {
		t.Errorf("PathMarkers = %v, want %v", m.PathMarkers, want)
	}
}

func TestWinGet(t *testing.T) {
	m := WinGet("winget upgrade SpecScore.CLI")
	if m.Name != "WinGet" {
		t.Errorf("Name = %q, want WinGet", m.Name)
	}
	want := []string{"/microsoft/winget/packages/", "/microsoft/winget/links/"}
	if !reflect.DeepEqual(m.PathMarkers, want) {
		t.Errorf("PathMarkers = %v, want %v", m.PathMarkers, want)
	}
}
