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

func TestWithExecutableUpgrade(t *testing.T) {
	m := Homebrew("brew upgrade --cask wb").WithExecutableUpgrade("brew", "upgrade", "--cask", "wb")

	if m.UpgradeExecutable != "brew" {
		t.Errorf("UpgradeExecutable = %q, want brew", m.UpgradeExecutable)
	}
	wantArgs := []string{"upgrade", "--cask", "wb"}
	if !reflect.DeepEqual(m.UpgradeArgs, wantArgs) {
		t.Errorf("UpgradeArgs = %v, want %v", m.UpgradeArgs, wantArgs)
	}
	if !m.CanExecuteUpgrade() {
		t.Error("CanExecuteUpgrade() = false, want true")
	}

	// The manager owns its own argv copy: callers may safely reuse or mutate
	// the input slice after configuring it.
	args := []string{"upgrade", "--cask", "tool"}
	configured := Homebrew("brew upgrade --cask tool").WithExecutableUpgrade("brew", args...)
	args[2] = "other"
	if got := configured.UpgradeArgs[2]; got != "tool" {
		t.Errorf("UpgradeArgs aliases caller input: got %q, want tool", got)
	}
}

func TestManagerWithoutExecutableUpgradeRemainsRedirectOnly(t *testing.T) {
	m := Homebrew("brew upgrade --cask wb")
	if m.CanExecuteUpgrade() {
		t.Error("legacy Homebrew manager unexpectedly executes its display command")
	}
}

func TestWithExecutableUpgradeStepsCopiesAndValidatesEveryStep(t *testing.T) {
	refreshArgs := []string{"update"}
	m := Homebrew("brew update && brew upgrade --cask wb").WithExecutableUpgradeSteps(
		ManagedCommand{Executable: "brew", Args: refreshArgs},
		ManagedCommand{Executable: "brew", Args: []string{"upgrade", "--cask", "wb"}},
	)
	refreshArgs[0] = "outdated"
	if !m.CanExecuteUpgrade() {
		t.Fatal("CanExecuteUpgrade() = false, want true for two valid steps")
	}
	if got := m.executableUpgradeSteps()[0].Args[0]; got != "update" {
		t.Errorf("first step aliases caller args: got %q, want update", got)
	}
	if m.UpgradeExecutable != "" || m.UpgradeArgs != nil {
		t.Errorf("legacy executable fields = %q %v, want cleared for ordered steps", m.UpgradeExecutable, m.UpgradeArgs)
	}

	invalid := Homebrew("invalid").WithExecutableUpgradeSteps(ManagedCommand{Args: []string{"update"}})
	if invalid.CanExecuteUpgrade() {
		t.Error("CanExecuteUpgrade() = true for a step with no executable")
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
