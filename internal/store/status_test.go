package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectStatusIsReadOnlyForAbsentAndLegacyDatabases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	status, err := InspectStatusAt(path, "mlb.l.1")
	if err != nil || status.DatabaseBytes != nil {
		t.Fatalf("absent status=%#v err=%v", status, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("absent status created database: %v", err)
	}
	db, conn := legacyConnection(t, path, 1)
	conn.Close()
	db.Close()
	before, _ := os.ReadFile(path)
	status, err = InspectStatusAt(path, "mlb.l.1")
	if err != nil || status.SchemaVersion == nil || *status.SchemaVersion != 1 {
		t.Fatalf("legacy status=%#v err=%v", status, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("legacy inspection mutated database")
	}
}

func TestInspectStatusReadsCurrentDashboardAndProviderState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.db")
	database, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.conn.ExecContext(testContext(), `INSERT INTO players(mlbam_id,yahoo_player_id,name,position_type,synced_at) VALUES(1,2,'One','H',100); UPDATE dashboard_status SET last_run_at=100,last_run_status='success',provider_failure_count=2,circuit_open=1,last_error='failure',provider_freshness_at=99 WHERE id=1; INSERT INTO sync_item_state(source,item,last_attempted_at,last_successful_at,status,error_message) VALUES('fangraphs','board',100,99,'complete','')`); err != nil {
		t.Fatal(err)
	}
	status, err := InspectStatusAt(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.MLBIdentityCount != 1 || status.YahooIdentityCount != 1 || status.LastRunStatus == nil || *status.LastRunStatus != "success" || status.FangraphsSync == nil {
		t.Fatalf("status=%#v", status)
	}
}

func TestProviderDashboardTransitionsBoundFailuresAndRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	database, err := OpenAtWithClock(path, testClock{value: time.Unix(2_000_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := database.RecordProviderFailure(strings.Repeat("failure ", 100)); err != nil {
			t.Fatal(err)
		}
	}
	status, err := InspectStatusAt(path, "")
	if err != nil || status.ProviderFailureCount != 5 || !status.CircuitOpen || status.ProviderLastError == nil || len([]rune(*status.ProviderLastError)) != 512 || status.LastRunStatus == nil || *status.LastRunStatus != "failed" {
		t.Fatalf("failed status=%#v err=%v", status, err)
	}
	if err := database.RecordProviderSuccess(true); err != nil {
		t.Fatal(err)
	}
	status, err = InspectStatusAt(path, "")
	if err != nil || status.ProviderFailureCount != 0 || status.CircuitOpen || status.ProviderLastError != nil || status.LastRunStatus == nil || *status.LastRunStatus != "degraded" || status.ProviderFreshnessAt == nil {
		t.Fatalf("recovered status=%#v err=%v", status, err)
	}
}
