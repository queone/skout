package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAbsenceCompatibilityAndPrivateAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	value, err := ReadAt(path)
	if err != nil || value != (Config{}) {
		t.Fatalf("absent ReadAt = %#v, %v", value, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"current_league":"mlb.l.1","current_team_key":"1.t.2","pull_public_league_id":"old"}`
	if err := os.WriteFile(path, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	value, err = ReadAt(path)
	if err != nil || value.PullPublicLeagueID != "old" {
		t.Fatalf("legacy ReadAt = %#v, %v", value, err)
	}
	if err := WriteAt(path, value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pull_public") {
		t.Fatalf("replacement retained deprecated field: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want 640", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files remain: %v", entries)
	}
}

func TestConfigRejectsMalformedAndTrailingJSONAndRequiresHome(t *testing.T) {
	for _, payload := range []string{"{", "{} {}", "{} trailing"} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadAt(path); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Errorf("ReadAt(%q) error = %v", payload, err)
		}
	}
	t.Setenv("HOME", "")
	if _, err := Path(); err == nil || !strings.Contains(err.Error(), "set HOME") {
		t.Fatalf("Path error = %v", err)
	}
}

func TestConfigNewPathUsesPrivateModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "config.json")
	if err := WriteAt(path, Config{CurrentLeague: "mlb.l.9"}); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode=%o want=%o", target, got, want)
		}
	}
}

func TestSyncAndStatusSelectionWritesRetainCompatiblePrivateJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "config.json")
	for _, value := range []Config{
		{CurrentLeague: "mlb.l.1", CurrentTeamKey: "mlb.l.1.t.2"},
		{CurrentLeague: "mlb.l.2"},
	} {
		if err := WriteAt(path, value); err != nil {
			t.Fatal(err)
		}
		read, err := ReadAt(path)
		if err != nil || read != value {
			t.Fatalf("read=%#v want=%#v err=%v", read, value, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "pull_public_league_id") {
			t.Fatalf("data=%q err=%v", data, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
		}
	}
}
