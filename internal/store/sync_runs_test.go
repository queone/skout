package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncRunLifecycleUsesDeterministicCountsAndOneCompletion(t *testing.T) {
	database, err := OpenAtWithClock(filepath.Join(t.TempDir(), "runs.db"), testClock{time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	id, err := database.StartSyncRun(SyncLive, OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := database.CompleteSyncRun(id, map[string]int64{"z": 2, "a": 1})
	if err != nil || !changed {
		t.Fatalf("complete=%v err=%v", changed, err)
	}
	changed, err = database.CompleteSyncRun(id, map[string]int64{})
	if err != nil || changed {
		t.Fatalf("second complete=%v err=%v", changed, err)
	}
	run, err := database.LatestSyncRun(SyncLive)
	if err != nil || run.Status != "complete" || run.Counts["a"] != 1 || run.Counts["z"] != 2 {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	failID, _ := database.StartSyncRun(SyncLive, OriginManual)
	changed, err = database.FailSyncRun(failID)
	if err != nil || !changed {
		t.Fatalf("fail=%v err=%v", changed, err)
	}
}

func testContext() context.Context { return context.Background() }
