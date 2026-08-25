package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

func TestStatusIsLocalOnlyNonPersistingAndDoesNotCreateDatabase(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	databasePath := filepath.Join(root, "absent.db")
	settings := config.Config{CurrentLeague: "saved.league", CurrentTeamKey: "saved.team"}
	if err := config.WriteAt(configPath, settings); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)
	output, err := Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Mode: terminal.Plain}, "temporary.league")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "League: temporary.league") || !strings.Contains(output, "Database: "+databasePath+" (absent, schema unknown)") {
		t.Fatalf("output=%s", output)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("status persisted override")
	}
	read, _ := config.ReadAt(configPath)
	if read.CurrentTeamKey != settings.CurrentTeamKey || read.CurrentLeague != settings.CurrentLeague {
		t.Fatalf("saved config=%#v", read)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("status created database: %v", err)
	}
}

func TestRenderStatusFixedOrderSuppressesLegacyYahooFailureAndBoundsErrors(t *testing.T) {
	version, size, timestamp, state := int64(6), int64(100), int64(99), "success"
	legacy := "fetch authenticated Yahoo: run skout login to reauthorize"
	status := store.Status{SchemaVersion: &version, DatabaseBytes: &size, MLBIdentityCount: 1, YahooIdentityCount: 1, LastRunAt: &timestamp, LastRunStatus: &state, ProviderFailureCount: 9, CircuitOpen: true, ProviderLastError: &legacy, ProviderFreshnessAt: &timestamp}
	output := RenderStatus("db", "cfg", config.Config{}, status, terminal.Plain)
	labels := []string{"Yahoo:", "Last run:", "Database:", "Identities:", "Provider freshness:", "FanGraphs:", "FantasyPros:", "Provider failures:", "Last provider error:", "Unmatched players:", "League:", "Config:"}
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
	output = RenderStatus("db", "cfg", config.Config{}, status, terminal.Plain)
	line, _, _ := strings.Cut(strings.Split(output, "Last provider error: ")[1], "\n")
	if len(line) != statusErrorLimit {
		t.Fatalf("bounded error length=%d", len(line))
	}
}
