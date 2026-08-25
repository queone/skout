package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandSnapshotsPreservePayloadAndStaleFallbackMetadata(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "snapshots.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	payload := `[{"id":1}]`
	if err := database.SaveCommandSnapshot("dataset", "source", "scope", "v1", payload); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.CommandSnapshot("dataset", "source", "scope")
	if err != nil || snapshot.Payload != payload || snapshot.Stale {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	changed, err := database.MarkCommandSnapshotStale("dataset", "source", "scope", "provider down")
	if err != nil || !changed {
		t.Fatalf("mark changed=%v err=%v", changed, err)
	}
	snapshot, _ = database.CommandSnapshot("dataset", "source", "scope")
	if !snapshot.Stale || snapshot.Payload != payload || snapshot.ErrorMessage != "provider down" {
		t.Fatalf("stale snapshot=%#v", snapshot)
	}
	if err := database.SaveCommandSnapshot("dataset", "source", "scope", "v1", `{bad`); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestFrozenV6FixtureContainsEveryScopedSnapshotAndPreservesUnrelatedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rust.db")
	database, err := OpenAtWithClock(path, testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	script, err := os.ReadFile("testdata/rust-v6.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(testContext(), string(script)); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][3]string{{"mlb_team_directory", "mlb", "2026"}, {"mlb_team_records", "mlb", "2026"}, {"mlb_team_roster", "mlb", "2026:NYY"}, {"mlb_team_totals", "mlb", "2026"}, {"mlb_current_odds", "espn", "2026-05-15"}, {"mlb_future_odds", "oddsshark", "2026-05-16"}, {"mlb_probable_pitchers", "mlb", "2026-05-15"}} {
		snapshot, err := database.CommandSnapshot(key[0], key[1], key[2])
		if err != nil || snapshot == nil || snapshot.SnapshotVersion != "v1" {
			t.Errorf("snapshot %v=%#v err=%v", key, snapshot, err)
		}
	}
	if err := database.SaveCommandSnapshot("mlb_team_directory", "mlb", "2026", "v1", `[]`); err != nil {
		t.Fatal(err)
	}
	var odds int64
	if err := database.conn.QueryRowContext(testContext(), "SELECT COUNT(*) FROM mlb_odds WHERE game_pk=800001").Scan(&odds); err != nil || odds != 1 {
		t.Fatalf("unrelated odds=%d err=%v", odds, err)
	}
}
