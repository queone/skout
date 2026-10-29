package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/queone/gkit/update"
	"github.com/queone/skout/internal/cache"
	"github.com/queone/skout/internal/terminal"
)

const (
	updateModule      = "github.com/queone/skout"
	updateInstallPath = "github.com/queone/skout/cmd/skout"
)

// checkUpdate asks the module proxy, at most once a day, whether a newer skout
// release exists; tests replace it to avoid network requests.
var checkUpdate = func(version string) (update.Result, error) {
	options, err := updateOptions(version)
	if err != nil {
		return update.Result{Current: options.Current}, err
	}
	return update.Check(options)
}

// updateOptions builds the update-check options for a program version, keeping
// the cache file beside the provider cache root.
func updateOptions(version string) (update.Options, error) {
	options := update.Options{Module: updateModule, Current: "v" + version}
	root, err := cache.ProductionRoot()
	if err != nil {
		return options, fmt.Errorf("update check: %w", err)
	}
	options.CacheFile = filepath.Join(filepath.Dir(root), "update-check")
	return options, nil
}

// updateStatus renders the styled value of the status update row. An available
// release uses the warning role, an error stays on one line, and only a release
// equal to the running version is named as the latest.
func updateStatus(result update.Result, err error, mode terminal.ColorMode) string {
	checked := result.CheckedAt.Local().Format(time.DateTime)
	var value string
	switch {
	case err != nil:
		value = "unknown (" + strings.ReplaceAll(err.Error(), "\n", "; ") + ")"
	case result.Skipped == update.SkippedRecentFailure && result.Latest == "":
		value = "unknown (" + result.Skipped + ")"
	case result.Skipped != "" && result.Skipped != update.SkippedRecentFailure:
		value = "skipped (" + result.Skipped + ")"
	case result.Available:
		return terminal.Warning(fmt.Sprintf("%s available; run go install %s@%s", result.Latest, updateInstallPath, result.Latest), mode)
	case result.Latest != result.Current:
		value = "none (checked " + checked + ")"
	default:
		value = fmt.Sprintf("none (%s is the latest, checked %s)", result.Latest, checked)
	}
	return terminal.Dim(value, mode)
}
