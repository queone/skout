package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/queone/gkit/update"
	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/terminal"
)

func TestUpdateStatusRendersEveryConventionValue(t *testing.T) {
	checkedAt := time.Date(2026, 10, 29, 15, 4, 5, 0, time.UTC)
	checked := checkedAt.Local().Format(time.DateTime)
	notice := "v0.16.0 available; run go install github.com/queone/skout/cmd/skout@v0.16.0"
	cases := []struct {
		name   string
		result update.Result
		err    error
		want   string
	}{
		{"available release", update.Result{Current: "v0.15.13", Latest: "v0.16.0", Available: true, CheckedAt: checkedAt}, nil, notice},
		{"latest equals running version", update.Result{Current: "v0.16.0", Latest: "v0.16.0", CheckedAt: checkedAt}, nil, "none (v0.16.0 is the latest, checked " + checked + ")"},
		{"latest older than running version", update.Result{Current: "v0.16.0", Latest: "v0.15.13", CheckedAt: checkedAt}, nil, "none (checked " + checked + ")"},
		{"no listed release", update.Result{Current: "v0.16.0", CheckedAt: checkedAt}, nil, "none (checked " + checked + ")"},
		{"opt-out variable set", update.Result{Current: "v0.16.0", Skipped: "NO_UPDATE_CHECK is set"}, nil, "skipped (NO_UPDATE_CHECK is set)"},
		{"development version", update.Result{Current: "vdev", Skipped: `version "vdev" is not a release`}, nil, `skipped (version "vdev" is not a release)`},
		{"recent failure with cached latest", update.Result{Current: "v0.16.0", Latest: "v0.16.0", CheckedAt: checkedAt, FromCache: true, Skipped: update.SkippedRecentFailure}, nil, "none (v0.16.0 is the latest, checked " + checked + ")"},
		{"recent failure with cached newer release", update.Result{Current: "v0.15.13", Latest: "v0.16.0", Available: true, CheckedAt: checkedAt, FromCache: true, Skipped: update.SkippedRecentFailure}, nil, notice},
		{"recent failure with nothing cached", update.Result{Current: "v0.16.0", Skipped: update.SkippedRecentFailure}, nil, "unknown (recent check failed)"},
		{"multi-line error", update.Result{Current: "v0.16.0"}, errors.Join(errors.New("proxy timeout"), errors.New("write cache")), "unknown (proxy timeout; write cache)"},
	}
	for _, test := range cases {
		if got := updateStatus(test.result, test.err, terminal.Plain); got != test.want {
			t.Errorf("%s: got %q want %q", test.name, got, test.want)
		}
	}
	colored := updateStatus(update.Result{Current: "v0.15.13", Latest: "v0.16.0", Available: true}, nil, terminal.Color)
	if colored != terminal.Warning(notice, terminal.Color) || strings.Contains(colored, "38;5;245") {
		t.Fatalf("available notice styling=%q", colored)
	}
	dimmed := updateStatus(update.Result{Current: "v0.16.0", Skipped: "NO_UPDATE_CHECK is set"}, nil, terminal.Color)
	if dimmed != terminal.Dim("skipped (NO_UPDATE_CHECK is set)", terminal.Color) {
		t.Fatalf("non-available styling=%q", dimmed)
	}
}

func TestUpdateOptionsKeepCacheBesideProviderCacheRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	options, err := updateOptions("0.16.0")
	if err != nil {
		t.Fatal(err)
	}
	root, err := cache.ProductionRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(root), "update-check")
	if options.Module != "github.com/queone/skout" || options.Current != "v0.16.0" || options.CacheFile != want {
		t.Fatalf("options=%#v want cache file %s", options, want)
	}
	if filepath.Base(filepath.Dir(options.CacheFile)) != "skout" {
		t.Fatalf("cache file outside the skout cache directory: %s", options.CacheFile)
	}
}

func TestUpdateCheckReportsUnknownWithoutCacheDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("NO_UPDATE_CHECK", "")
	result, err := checkUpdate("0.16.0")
	if err == nil {
		t.Fatalf("missing cache directory returned result %#v", result)
	}
	if got := updateStatus(result, err, terminal.Plain); !strings.HasPrefix(got, "unknown (update check: resolve cache root:") {
		t.Fatalf("status=%q", got)
	}
}

func TestUpdateCheckSkipsWithoutNetworkWhenOptedOutOrUnreleased(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("NO_UPDATE_CHECK", "1")
	result, err := checkUpdate("0.16.0")
	if got := updateStatus(result, err, terminal.Plain); got != "skipped (NO_UPDATE_CHECK is set)" {
		t.Fatalf("opt-out status=%q", got)
	}
	t.Setenv("NO_UPDATE_CHECK", "")
	result, err = checkUpdate("dev")
	if got := updateStatus(result, err, terminal.Plain); got != `skipped (version "vdev" is not a release)` {
		t.Fatalf("development status=%q", got)
	}
}
