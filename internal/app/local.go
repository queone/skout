package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/terminal"
)

const statusErrorLimit = 240

// LocalOptions contains the injected local-only status dependencies.
type LocalOptions struct {
	ConfigPath   string
	DatabasePath string
	Now          func() time.Time
	Mode         terminal.ColorMode
}

// StatusProduction renders status and persists an explicit league selection.
func StatusProduction(requestedLeague string, mode terminal.ColorMode) (string, error) {
	configPath, err := config.Path()
	if err != nil {
		return "", fmt.Errorf("status: resolve configuration path: %w", err)
	}
	databasePath, err := store.DatabasePath()
	if err != nil {
		return "", fmt.Errorf("status: resolve database path: %w", err)
	}
	guard, err := store.AcquireDatabaseGuard(databasePath, store.DatabaseGuardShared)
	if err != nil {
		return "", fmt.Errorf("status: acquire database operation guard: %w", err)
	}
	output, statusErr := Status(LocalOptions{ConfigPath: configPath, DatabasePath: databasePath, Now: time.Now, Mode: mode}, requestedLeague)
	guardErr := guard.Close()
	if statusErr != nil || guardErr != nil {
		return "", errors.Join(statusErr, func() error {
			if guardErr == nil {
				return nil
			}
			return fmt.Errorf("status: release database operation guard: %w", guardErr)
		}())
	}
	return output, nil
}

// Status renders the fixed-order local dashboard and persists an explicit league selection.
func Status(options LocalOptions, requestedLeague string) (string, error) {
	if options.ConfigPath == "" || options.DatabasePath == "" {
		return "", fmt.Errorf("status: local paths are incomplete; configure both paths and retry")
	}
	settings, err := config.ReadAt(options.ConfigPath)
	if err != nil {
		return "", fmt.Errorf("status: read configuration: %w", err)
	}
	if selected := strings.TrimSpace(requestedLeague); selected != "" && settings.CurrentLeague != selected {
		settings.CurrentLeague = selected
		settings.CurrentTeamKey = ""
		if err := config.WriteAt(options.ConfigPath, settings); err != nil {
			return "", fmt.Errorf("status: save league selection: %w", err)
		}
	}
	status, err := store.InspectStatusAt(options.DatabasePath, settings.CurrentLeague)
	if err != nil {
		return "", fmt.Errorf("status: inspect database: %w", err)
	}
	return RenderStatus(options.DatabasePath, options.ConfigPath, settings, status, options.Mode), nil
}

// RenderStatus renders the frozen status fields in their contracted order.
func RenderStatus(databasePath, configPath string, settings config.Config, status store.Status, mode terminal.ColorMode) string {
	hasSnapshot := status.MLBIdentityCount > 0 || status.UnmatchedPlayerCount > 0
	legacyFailure := status.ProviderLastError != nil && isLegacyYahooFailure(*status.ProviderLastError)
	lastRun := "none"
	if !legacyFailure && status.LastRunStatus != nil && status.LastRunAt != nil {
		lastRun = fmt.Sprintf("%s at unix %d", *status.LastRunStatus, *status.LastRunAt)
	}
	databaseSize := "absent"
	if status.DatabaseBytes != nil {
		databaseSize = fmt.Sprintf("%d bytes", *status.DatabaseBytes)
	}
	schema := "unknown"
	if status.SchemaVersion != nil {
		schema = fmt.Sprintf("v%d", *status.SchemaVersion)
	}
	identities := "unavailable"
	freshness := "unavailable"
	unmatched := "unavailable"
	if hasSnapshot {
		identities = fmt.Sprintf("%d MLB, %d Yahoo", status.MLBIdentityCount, status.YahooIdentityCount)
		freshness = "none"
		if status.ProviderFreshnessAt != nil {
			freshness = fmt.Sprintf("unix %d", *status.ProviderFreshnessAt)
		}
		unmatched = fmt.Sprintf("%d", status.UnmatchedPlayerCount)
	}
	failureCount := status.ProviderFailureCount
	circuitOpen := status.CircuitOpen
	lastError := "none"
	if !legacyFailure {
		if status.ProviderLastError != nil {
			lastError = boundText(*status.ProviderLastError, statusErrorLimit)
		}
	} else {
		failureCount = 0
		circuitOpen = false
	}
	league := terminal.Injury("none", mode)
	if settings.CurrentLeague != "" {
		league = terminal.Dim(settings.CurrentLeague, mode)
	}
	providerState := "ready"
	if circuitOpen {
		providerState = "blocked"
	}
	value := func(pointer *string) string {
		if pointer == nil {
			return "none"
		}
		return *pointer
	}
	rows := [][2]string{
		{"Yahoo", "public endpoints"},
		{"Last run", lastRun},
		{"Database", fmt.Sprintf("%s (%s, schema %s)", filepath.Clean(databasePath), databaseSize, schema)},
		{"Identities", identities},
		{"Provider freshness", freshness},
		{"FanGraphs", value(status.FangraphsSync)},
		{"FantasyPros", value(status.FantasyProsSync)},
		{"Provider failures", fmt.Sprintf("%s (%d)", providerState, failureCount)},
		{"Last provider error", lastError},
		{"Unmatched players", unmatched},
		{"League", league},
		{"Config", terminal.Dim(filepath.Clean(configPath), mode)},
	}
	var output strings.Builder
	for _, row := range rows {
		value := row[1]
		if row[0] != "League" && row[0] != "Config" {
			value = terminal.Dim(value, mode)
		}
		fmt.Fprintf(&output, "%s: %s\n", row[0], value)
	}
	if !hasSnapshot {
		output.WriteString("No local snapshot; run skout sync.\n")
	}
	if status.SavantBBEUnavailable {
		output.WriteString(terminal.Dim("Savant: EV/BRL%/HH%/GB% unavailable — Baseball Savant's own export is not currently populating BBE", mode))
		output.WriteByte('\n')
	}
	return output.String()
}

func isLegacyYahooFailure(value string) bool {
	folded := strings.ToLower(value)
	return strings.Contains(folded, "fetch authenticated yahoo") || strings.Contains(folded, "run skout login") || strings.Contains(folded, "reauthorize")
}

func boundText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
