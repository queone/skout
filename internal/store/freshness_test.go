package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type adjustableStoreClock struct{ value time.Time }

func (clock *adjustableStoreClock) Now() time.Time { return clock.value }

func TestItemFreshnessGatesForceVersionAndRetainsPriorSuccess(t *testing.T) {
	clock := &adjustableStoreClock{value: time.Unix(100, 0)}
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "freshness.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	policy := ItemRefreshPolicy{TTL: 30 * time.Minute, PipelineVersion: "provider-sync-v1"}
	needs, err := database.NeedsSyncItem("mlb", "hitting", "2026", policy)
	if err != nil || !needs {
		t.Fatalf("new needs=%v err=%v", needs, err)
	}
	if err := database.MarkSyncItemSuccess("mlb", "hitting", "2026", policy.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	state, err := database.SyncItemState("mlb", "hitting", "2026")
	if err != nil || state == nil || state.Status != SyncStateComplete || state.LastAttemptedAt.Unix() != 100 || state.LastSuccessfulAt.Unix() != 100 || state.ErrorMessage != "" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	clock.value = time.Unix(150, 0)
	if err := database.MarkSyncItemAttempt("mlb", "hitting", "2026", policy.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncItemState("mlb", "hitting", "2026")
	if err != nil || state.Status != SyncStateRunning || state.LastAttemptedAt.Unix() != 150 || state.LastSuccessfulAt.Unix() != 100 {
		t.Fatalf("attempt state=%#v err=%v", state, err)
	}
	if err := database.MarkSyncItemSuccess("mlb", "hitting", "2026", policy.PipelineVersion); err != nil {
		t.Fatal(err)
	}
	clock.value = time.Unix(1950, 0)
	needs, err = database.NeedsSyncItem("mlb", "hitting", "2026", policy)
	if err != nil || needs {
		t.Fatalf("boundary needs=%v err=%v", needs, err)
	}
	clock.value = time.Unix(1951, 0)
	needs, err = database.NeedsSyncItem("mlb", "hitting", "2026", policy)
	if err != nil || !needs {
		t.Fatalf("expired needs=%v err=%v", needs, err)
	}
	forced := policy
	forced.Force = true
	needs, err = database.NeedsSyncItem("mlb", "hitting", "2026", forced)
	if err != nil || !needs {
		t.Fatalf("forced needs=%v err=%v", needs, err)
	}
	changed := policy
	changed.PipelineVersion = "provider-sync-v2"
	needs, err = database.NeedsSyncItem("mlb", "hitting", "2026", changed)
	if err != nil || !needs {
		t.Fatalf("version needs=%v err=%v", needs, err)
	}
	clock.value = time.Unix(2000, 0)
	if err := database.MarkSyncItemFailure("mlb", "hitting", "2026", policy.PipelineVersion, "offline"); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncItemState("mlb", "hitting", "2026")
	if err != nil || state.Status != SyncStateFailed || state.LastAttemptedAt.Unix() != 2000 || state.LastSuccessfulAt.Unix() != 150 || state.ErrorMessage != "offline" {
		t.Fatalf("failure state=%#v err=%v", state, err)
	}
	clock.value = time.Unix(2100, 0)
	if err := database.MarkSyncItemDegraded("mlb", "hitting", "2026", policy.PipelineVersion, strings.Repeat("é", 300)); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncItemState("mlb", "hitting", "2026")
	if err != nil || state.Status != SyncStateComplete || state.LastSuccessfulAt.Unix() != 2100 || len([]rune(state.ErrorMessage)) != freshnessDetailLimit {
		t.Fatalf("degraded state=%#v err=%v", state, err)
	}
}

func TestRowFreshnessRetainsSuccessAndValidatesIdentity(t *testing.T) {
	clock := &adjustableStoreClock{value: time.Unix(100, 0)}
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "rows.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	localID := int64(7)
	if err := database.MarkSyncRowSuccess("mlb", "roster", "2026", "team", "NYY", &localID, "provider-sync-v1"); err != nil {
		t.Fatal(err)
	}
	state, err := database.SyncRowState("mlb", "roster", "2026", "team", "NYY")
	if err != nil || state == nil || state.LocalID == nil || *state.LocalID != 7 || state.Status != SyncStateComplete {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	policy := RowRefreshPolicy{TTL: 30 * time.Minute, PipelineVersion: "provider-sync-v1"}
	needs, err := database.NeedsSyncRow("mlb", "roster", "2026", "team", "NYY", policy)
	if err != nil || needs {
		t.Fatalf("needs=%v err=%v", needs, err)
	}
	clock.value = time.Unix(150, 0)
	if err := database.MarkSyncRowAttempt("mlb", "roster", "2026", "team", "NYY", &localID, "provider-sync-v1"); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncRowState("mlb", "roster", "2026", "team", "NYY")
	if err != nil || state.Status != SyncStateRunning || state.LastSuccessfulAt.Unix() != 100 {
		t.Fatalf("attempt state=%#v err=%v", state, err)
	}
	if err := database.MarkSyncRowDegraded("mlb", "roster", "2026", "team", "NYY", &localID, "provider-sync-v1", strings.Repeat("x", 300)); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncRowState("mlb", "roster", "2026", "team", "NYY")
	if err != nil || state.Status != SyncStateComplete || state.LastSuccessfulAt.Unix() != 150 || len(state.ErrorMessage) != freshnessDetailLimit {
		t.Fatalf("degraded state=%#v err=%v", state, err)
	}
	clock.value = time.Unix(200, 0)
	if err := database.MarkSyncRowFailure("mlb", "roster", "2026", "team", "NYY", nil, "provider-sync-v1", "timeout"); err != nil {
		t.Fatal(err)
	}
	state, err = database.SyncRowState("mlb", "roster", "2026", "team", "NYY")
	if err != nil || state.Status != SyncStateFailed || state.LocalID != nil || state.LastSuccessfulAt.Unix() != 150 || state.ErrorMessage != "timeout" {
		t.Fatalf("failure state=%#v err=%v", state, err)
	}
	invalidID := int64(0)
	if err := database.MarkSyncRowSuccess("mlb", "roster", "", "team", "BOS", &invalidID, "v1"); err == nil {
		t.Fatal("zero local ID accepted")
	}
	if _, err := database.NeedsSyncItem("", "hitting", "", ItemRefreshPolicy{TTL: time.Minute, PipelineVersion: "v1"}); err == nil {
		t.Fatal("empty source accepted")
	}
	if _, err := database.NeedsSyncRow("mlb", "roster", "", "team", "BOS", RowRefreshPolicy{TTL: -1, PipelineVersion: "v1"}); err == nil {
		t.Fatal("negative TTL accepted")
	}
}

func TestFreshnessInjectedWriteFailureRollsBack(t *testing.T) {
	clock := &adjustableStoreClock{value: time.Unix(100, 0)}
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "rollback.db"), clock)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.MarkSyncItemSuccess("mlb", "hitting", "", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(testContext(), `CREATE TRIGGER reject_item_update BEFORE UPDATE ON sync_item_state BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	clock.value = time.Unix(200, 0)
	err = database.MarkSyncItemFailure("mlb", "hitting", "", "v2", strings.Repeat("x", 300))
	if err == nil {
		t.Fatal("injected update succeeded")
	}
	state, err := database.SyncItemState("mlb", "hitting", "")
	if err != nil || state.Status != SyncStateComplete || state.PipelineVersion != "v1" || state.LastSuccessfulAt.Unix() != 100 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}
