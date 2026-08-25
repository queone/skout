package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSeasonCompletenessStatesAndPipelineInvalidation(t *testing.T) {
	clock := &adjustableStoreClock{value: time.Unix(100, 0)}
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "seasons.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	complete, err := database.IsSeasonComplete("mlbam_hitting", 2025, 1)
	if err != nil || complete {
		t.Fatalf("missing complete=%v err=%v", complete, err)
	}
	if err := database.MarkSeasonComplete("mlbam_hitting", 2025, 250, 1); err != nil {
		t.Fatal(err)
	}
	state, err := database.SeasonState("mlbam_hitting", 2025)
	if err != nil || state == nil || state.Status != SeasonComplete || state.FetchedAt.Unix() != 100 || state.RecordCount != 250 || state.PipelineVersion != 1 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	for version, want := range map[int64]bool{0: true, 1: true, 2: false} {
		complete, err = database.IsSeasonComplete("mlbam_hitting", 2025, version)
		if err != nil || complete != want {
			t.Errorf("version=%d complete=%v want=%v err=%v", version, complete, want, err)
		}
	}
	clock.value = time.Unix(200, 0)
	if err := database.MarkSeasonPartial("mlbam_hitting", 2025, 199, 2); err != nil {
		t.Fatal(err)
	}
	state, _ = database.SeasonState("mlbam_hitting", 2025)
	complete, _ = database.IsSeasonComplete("mlbam_hitting", 2025, 1)
	if state.Status != SeasonPartial || complete {
		t.Fatalf("partial state=%#v complete=%v", state, complete)
	}
	clock.value = time.Unix(300, 0)
	if err := database.MarkSeasonFailed("mlbam_hitting", 2025, 0, 2); err != nil {
		t.Fatal(err)
	}
	state, _ = database.SeasonState("mlbam_hitting", 2025)
	if state.Status != SeasonFailed || state.RecordCount != 0 || state.FetchedAt.Unix() != 300 {
		t.Fatalf("failed state=%#v", state)
	}
	if err := database.MarkSeasonComplete("", 2025, 1, 1); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := database.MarkSeasonComplete("mlb", 2025, -1, 1); err == nil {
		t.Fatal("negative record count accepted")
	}
}

func TestSeasonInjectedWriteFailurePreservesPriorRow(t *testing.T) {
	clock := &adjustableStoreClock{value: time.Unix(100, 0)}
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "season-rollback.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.MarkSeasonComplete("mlb", 2026, 20, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(testContext(), `CREATE TRIGGER reject_season_update BEFORE UPDATE ON season_sync_status BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	clock.value = time.Unix(200, 0)
	if err := database.MarkSeasonFailed("mlb", 2026, 5, 2); err == nil {
		t.Fatal("injected update succeeded")
	}
	state, err := database.SeasonState("mlb", 2026)
	if err != nil || state.Status != SeasonComplete || state.RecordCount != 20 || state.PipelineVersion != 1 || state.FetchedAt.Unix() != 100 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}
