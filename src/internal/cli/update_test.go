package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPackageManagerHint(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantHint bool
	}{
		{"homebrew cask (symlink resolved)", "/opt/homebrew/Caskroom/apiary/0.3.0/apiary", true},
		{"homebrew cellar", "/usr/local/Cellar/apiary/0.3.0/bin/apiary", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/apiary", true},
		{"scoop", `C:\Users\orlando\scoop\apps\apiary\current\apiary.exe`, true},
		{"manual /usr/local/bin", "/usr/local/bin/apiary", false},
		{"manual home dir", "/home/orlando/bin/apiary", false},
		{"windows manual", `C:\tools\apiary\apiary.exe`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint := packageManagerHint(tt.path)
			if (hint != "") != tt.wantHint {
				t.Errorf("packageManagerHint(%q) = %q, want hint=%v", tt.path, hint, tt.wantHint)
			}
		})
	}
}

func TestNewerVersionAvailable(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.3.0", "0.4.0", true},
		{"0.3.0", "0.3.0", false},
		{"0.4.0", "0.3.0", false},
		{"0.1.0-dev", "0.1.0", true}, // dev prerelease sorts below the release
		{"0.3.0", "", false},
		{"not-a-version", "0.4.0", false},
		{"0.3.0", "not-a-version", false},
		{"0.3.0", "v0.4.0", true}, // tolerate a v prefix
	}
	for _, tt := range tests {
		if got := newerVersionAvailable(tt.current, tt.latest); got != tt.want {
			t.Errorf("newerVersionAvailable(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestUpdateCheckStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update-check.json")

	// Missing file → zero state (stale, triggers a refresh).
	state := loadUpdateCheckState(path)
	if !state.CheckedAt.IsZero() || state.Latest != "" {
		t.Fatalf("expected zero state for missing file, got %+v", state)
	}

	want := updateCheckState{CheckedAt: time.Now().Truncate(time.Second), Latest: "0.4.0"}
	saveUpdateCheckState(path, want)

	got := loadUpdateCheckState(path)
	if !got.CheckedAt.Equal(want.CheckedAt) || got.Latest != want.Latest {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestLoadUpdateCheckStateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	saveUpdateCheckState(path, updateCheckState{Latest: "0.4.0"})

	// Corrupt the file and make sure loading degrades to the zero state.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := loadUpdateCheckState(path)
	if state.Latest != "" || !state.CheckedAt.IsZero() {
		t.Errorf("expected zero state for corrupt file, got %+v", state)
	}
}
