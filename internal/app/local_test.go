package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/queone/gkit/update"
	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

func TestStatusPersistsExplicitLeagueClearsTeamAndDoesNotCreateDatabase(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	databasePath := filepath.Join(root, "absent.db")
	settings := config.Config{CurrentLeague: "saved.league", CurrentTeamKey: "saved.team"}
	if err := config.WriteAt(configPath, settings); err != nil {
		t.Fatal(err)
	}
	output, err := Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Mode: terminal.Plain}, "temporary.league")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "League: temporary.league") || !strings.Contains(output, "Database: "+databasePath+" (absent, schema unknown)") {
		t.Fatalf("output=%s", output)
	}
	read, _ := config.ReadAt(configPath)
	if read.CurrentTeamKey != "" || read.CurrentLeague != "temporary.league" {
		t.Fatalf("saved config=%#v", read)
	}
	info, err := os.Stat(configPath)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions=%v err=%v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("status created database: %v", err)
	}
}

func TestStatusWithoutChangedSelectionDoesNotRewriteConfiguration(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	databasePath := filepath.Join(root, "absent.db")
	settings := config.Config{CurrentLeague: "saved.league", CurrentTeamKey: "saved.team"}
	if err := config.WriteAt(configPath, settings); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"", "saved.league"} {
		if _, err := Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Mode: terminal.Plain}, requested); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("unchanged selection rewrote configuration: before=%q after=%q", before, after)
	}
}

func TestStatusProductionSharesDatabaseGuardAndKeepsInjectedStatusReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	checks := stubUpdateCheck(t, update.Result{Current: "v0.16.0", Skipped: "NO_UPDATE_CHECK is set"}, nil)
	databasePath, err := store.DatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	exclusive, err := store.AcquireDatabaseGuard(databasePath, store.DatabaseGuardExclusive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StatusProduction("", "0.16.0", terminal.Plain); err == nil || !strings.Contains(err.Error(), "another skout command") {
		t.Fatalf("status during exclusive guard: %v", err)
	}
	if *checks != 1 {
		t.Fatalf("update check ran %d times before the database guard", *checks)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := StatusProduction("", "0.16.0", terminal.Plain)
	if err != nil || !strings.HasSuffix(strings.Split(output, "No local snapshot")[0], "Update: skipped (NO_UPDATE_CHECK is set)\n") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("status created database: %v", err)
	}
}

func TestRenderStatusFixedOrderSuppressesLegacyYahooFailureAndBoundsErrors(t *testing.T) {
	version, size, timestamp, state := int64(6), int64(100), int64(99), "success"
	legacy := "fetch authenticated Yahoo: run skout login to reauthorize"
	status := store.Status{SchemaVersion: &version, DatabaseBytes: &size, MLBIdentityCount: 1, YahooIdentityCount: 1, LastRunAt: &timestamp, LastRunStatus: &state, ProviderFailureCount: 9, CircuitOpen: true, ProviderLastError: &legacy, ProviderFreshnessAt: &timestamp}
	output := RenderStatus("db", "cfg", config.Config{}, status, "none (checked now)", terminal.Plain)
	labels := []string{"Yahoo:", "Last run:", "Database:", "Identities:", "Provider freshness:", "FanGraphs:", "FantasyPros:", "Provider failures:", "Last provider error:", "Unmatched players:", "League:", "Config:", "Update:"}
	position := -1
	for _, label := range labels {
		next := strings.Index(output, label)
		if next <= position {
			t.Fatalf("field %s out of order in %s", label, output)
		}
		position = next
	}
	if !strings.Contains(output, "Last run: none") || !strings.Contains(output, "Provider failures: ready (0)") || !strings.Contains(output, "Last provider error: none") {
		t.Fatalf("legacy output=%s", output)
	}
	long := strings.Repeat("x", 300)
	status.ProviderLastError = &long
	output = RenderStatus("db", "cfg", config.Config{}, status, "none (checked now)", terminal.Plain)
	line, _, _ := strings.Cut(strings.Split(output, "Last provider error: ")[1], "\n")
	if len(line) != statusErrorLimit {
		t.Fatalf("bounded error length=%d", len(line))
	}
}

func TestStatusRendersUpdateRowLastWithoutFailingStatus(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	databasePath := filepath.Join(root, "absent.db")
	if err := config.WriteAt(configPath, config.Config{CurrentLeague: "saved.league"}); err != nil {
		t.Fatal(err)
	}
	unknown := "unknown (proxy timeout; write cache)"
	output, err := Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Mode: terminal.Plain, Update: func() string { return unknown }}, "")
	if err != nil {
		t.Fatalf("unknown update check failed status: %v", err)
	}
	rows := strings.Split(strings.Split(output, "No local snapshot")[0], "\n")
	if len(rows) < 3 || !strings.HasPrefix(rows[len(rows)-3], "Config: ") || rows[len(rows)-2] != "Update: "+unknown {
		t.Fatalf("update row is not the final field after Config: %q", output)
	}
	output, err = Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Mode: terminal.Plain}, "")
	if err != nil || !strings.Contains(output, "\nUpdate: skipped (update check unavailable)\n") {
		t.Fatalf("nil update output=%q err=%v", output, err)
	}
}

func TestRenderStatusKeepsAvailableUpdateUndimmed(t *testing.T) {
	notice := terminal.Warning("v0.16.0 available; run go install github.com/queone/skout/cmd/skout@v0.16.0", terminal.Color)
	output := RenderStatus("db", "cfg", config.Config{}, store.Status{}, notice, terminal.Color)
	if !strings.Contains(output, "Update: "+notice+"\n") {
		t.Fatalf("available update was restyled: %q", output)
	}
}

// stubUpdateCheck replaces the network update check for one test and counts its calls.
func stubUpdateCheck(t *testing.T, result update.Result, err error) *int {
	t.Helper()
	calls := 0
	original := checkUpdate
	checkUpdate = func(string) (update.Result, error) {
		calls++
		return result, err
	}
	t.Cleanup(func() { checkUpdate = original })
	return &calls
}
