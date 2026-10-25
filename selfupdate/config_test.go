package selfupdate

import "testing"

// platformSupported has three distinct outcomes: an empty SupportedPlatforms
// list supports everything, a populated list containing the host's own
// goosName/goarchName pair matches (the "supported" case), and a populated
// list that doesn't contain it falls through to unsupported.
func TestPlatformSupported(t *testing.T) {
	origOS, origArch := goosName, goarchName
	t.Cleanup(func() { goosName, goarchName = origOS, origArch })
	goosName, goarchName = "linux", "amd64"

	t.Run("empty list supports every platform", func(t *testing.T) {
		cfg := Config{}.withDefaults()
		if !cfg.platformSupported() {
			t.Error("platformSupported() = false, want true for an empty SupportedPlatforms list")
		}
	})

	t.Run("matching platform is supported", func(t *testing.T) {
		cfg := Config{SupportedPlatforms: []Platform{
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
		}}.withDefaults()
		if !cfg.platformSupported() {
			t.Error("platformSupported() = false, want true when the host platform is listed")
		}
	})

	t.Run("non-matching platform is unsupported", func(t *testing.T) {
		cfg := Config{SupportedPlatforms: []Platform{
			{GOOS: "windows", GOARCH: "386"},
		}}.withDefaults()
		if cfg.platformSupported() {
			t.Error("platformSupported() = true, want false when the host platform is not listed")
		}
	})
}
