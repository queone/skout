package app

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
	"github.com/queone/skout/internal/transport"
)

const historicalSeasonPipelineVersion int64 = 1

type syncSourceSet struct {
	Force            bool
	MLBHitting       func(int64, string) ([]providers.BulkHittingSplit, error)
	MLBPitching      func(int64, string) ([]providers.BulkPitchingSplit, error)
	MLBQualityStarts func(int64, []int64) (providers.QualityStartResult, error)
	MLBDirectory     func(int64) ([]providers.TeamDirectoryEntry, error)
	MLBRoster        func(int64) ([]providers.RosterMember, error)
	SavantBatting    func(int64) ([]providers.SavantRow, error)
	SavantPitching   func(int64) ([]providers.SavantRow, error)
	FanGraphs        func(int64) (providers.FanGraphsSnapshot, error)
	Closers          func() ([]providers.CloserChartEntry, error)
	FantasyPros      func() ([]providers.ECRRow, error)
	ESPN             func(time.Time) (providers.ESPNSlateLines, error)
}

// SyncProduction runs the public foreground synchronization against production paths.
func SyncProduction(options SyncOptions) (string, error) {
	configPath, err := config.Path()
	if err != nil {
		return "", fmt.Errorf("sync: resolve configuration: %w", err)
	}
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	if err := writeSyncOutput(output, "==> Sync started.\n"); err != nil {
		return "", fmt.Errorf("sync: write progress: %w", err)
	}
	runtimeDirectory := filepath.Join(filepath.Dir(configPath), "runtime")
	guard, err := acquireSyncGuard(runtimeDirectory)
	if err != nil {
		return "", err
	}
	defer guard.release()
	database, err := store.Open()
	if err != nil {
		return "", fmt.Errorf("sync: open database: %w", err)
	}
	defer database.Close()

	http := transport.Production()
	yahoo := providers.NewProductionYahooPublicClient(http)
	mlb := providers.NewProductionMLBClient(http)
	savant := providers.NewProductionSavantClient(http)
	fanGraphs := providers.NewProductionFanGraphsClient(http)
	closers := providers.NewProductionFanGraphsCloserClient(http)
	fantasyPros := providers.NewProductionFantasyProsClient(http)
	espn := providers.NewProductionESPNClient(http, options.Version)
	now := time.Now().UTC()
	season, err := syncSeason(database, configPath, options.League, now)
	if err != nil {
		return "", err
	}
	origin := store.OriginManual
	if options.Auto {
		origin = store.OriginAutomatic
	}
	sources := syncSourceSet{
		Force:      !options.Auto,
		MLBHitting: mlb.FetchBulkHittingStats, MLBPitching: mlb.FetchBulkPitchingStats,
		MLBQualityStarts: mlb.FetchQualityStarts, MLBDirectory: mlb.FetchTeamDirectory,
		MLBRoster: mlb.FetchRoster, SavantBatting: savant.FetchBatting,
		SavantPitching: savant.FetchPitching, FanGraphs: fanGraphs.FetchSnapshot,
		Closers: closers.FetchCloserChart, FantasyPros: fantasyPros.FetchECR,
		ESPN: espn.FetchGameLines,
	}
	service := SyncService{
		Store: database, Yahoo: yahoo, Steps: buildSyncSteps(season, now, sources),
		ConfigPath: configPath, RuntimeDirectory: runtimeDirectory,
		CanonicalLeague: providers.CanonicalPublicLeagueKey,
		Input:           options.Input, Prompt: options.Prompt, Output: output,
		InputTerminal: options.InputTerminal, OutputTerminal: options.OutputTerminal, Origin: origin,
		lockHeld: true, startReported: true,
	}
	if service.Input == nil {
		service.Input = strings.NewReader("")
	}
	if service.Prompt == nil {
		service.Prompt = io.Discard
	}
	return service.Run(options.League, options.Team)
}

func syncSeason(database *store.Store, configPath, override string, now time.Time) (int64, error) {
	requested := strings.TrimSpace(override)
	if requested == "" {
		settings, err := config.ReadAt(configPath)
		if err != nil {
			return 0, fmt.Errorf("sync: read configuration: %w", err)
		}
		requested = strings.TrimSpace(settings.CurrentLeague)
	}
	if requested != "" {
		leagueKey, err := providers.CanonicalPublicLeagueKey(requested)
		if err == nil {
			season, seasonErr := database.FantasySeason(leagueKey)
			if seasonErr != nil {
				return 0, fmt.Errorf("sync: read fantasy season: %w", seasonErr)
			}
			if season != nil && *season > 0 {
				return int64(*season), nil
			}
		}
	}
	return int64(now.UTC().Year()), nil
}

func buildSyncSteps(season int64, now time.Time, sources syncSourceSet) []SyncStep {
	scope := strconv.FormatInt(season, 10)
	steps := []SyncStep{
		{Source: "mlb", Item: "hitting", Scope: scope, Run: func(database *store.Store) (SyncStepResult, error) {
			rows, err := sources.MLBHitting(season, "R")
			if err != nil {
				return SyncStepResult{}, fmt.Errorf("fetch MLB hitting: %w; prior hitting data was retained", err)
			}
			writes := hittingSyncWrites(rows)
			if err := database.ReplaceMLBSeasonStats(season, writes); err != nil {
				return SyncStepResult{}, fmt.Errorf("persist MLB hitting: %w; prior hitting data was retained", err)
			}
			if _, err := database.ReconcileMLBIdentities(hittingIdentityCandidates(rows)); err != nil {
				return SyncStepResult{}, fmt.Errorf("reconcile MLB hitting identities: %w", err)
			}
			return SyncStepResult{Count: int64(len(writes))}, nil
		}},
		{Source: "mlb", Item: "pitching", Scope: scope, Run: func(database *store.Store) (SyncStepResult, error) {
			rows, err := sources.MLBPitching(season, "R")
			if err != nil {
				return SyncStepResult{}, fmt.Errorf("fetch MLB pitching: %w; prior pitching data was retained", err)
			}
			pitcherIDs := make([]int64, 0, len(rows))
			for _, row := range rows {
				if row.Player.PersonID > 0 && row.Stat.GamesStarted > 0 {
					pitcherIDs = append(pitcherIDs, row.Player.PersonID)
				}
			}
			qualityStarts, err := sources.MLBQualityStarts(season, pitcherIDs)
			if err != nil {
				return SyncStepResult{}, fmt.Errorf("fetch MLB quality starts: %w; prior pitching data was retained", err)
			}
			writes := pitchingSyncWrites(rows, qualityStarts.Counts)
			if err := database.ReplaceMLBSeasonStats(season, writes); err != nil {
				return SyncStepResult{}, fmt.Errorf("persist MLB pitching: %w; prior pitching data was retained", err)
			}
			if _, err := database.ReconcileMLBIdentities(pitchingIdentityCandidates(rows)); err != nil {
				return SyncStepResult{}, fmt.Errorf("reconcile MLB pitching identities: %w", err)
			}
			result := SyncStepResult{Count: int64(len(writes))}
			if len(qualityStarts.Issues) > 0 {
				result.Degraded = true
				result.Detail = fmt.Sprintf("%d bounded quality-start issues; prior nonzero values were retained", len(qualityStarts.Issues))
			}
			return result, nil
		}},
	}
	for historicalSeason := season - 5; historicalSeason < season; historicalSeason++ {
		for _, group := range []string{"hitting", "pitching"} {
			steps = append(steps, historicalMLBStep(historicalSeason, group, sources))
		}
	}
	steps = append(steps,
		mlbRosterStep(season, sources),
		savantStep(season, "batting", sources.SavantBatting),
		savantStep(season, "pitching", sources.SavantPitching),
		fanGraphsStep(season, sources),
		fantasyProsStep(season, sources.FantasyPros),
		espnStep(now, sources.ESPN),
	)
	return steps
}

func historicalMLBStep(season int64, group string, sources syncSourceSet) SyncStep {
	source := "mlbam_" + group
	item := fmt.Sprintf("%d_%s", season, group)
	return SyncStep{
		Source: "mlb_history", Item: item, Scope: strconv.FormatInt(season, 10),
		Skip: func(database *store.Store) (bool, error) {
			return database.IsSeasonComplete(source, season, historicalSeasonPipelineVersion)
		},
		Run: func(database *store.Store) (SyncStepResult, error) {
			var writes []store.SeasonStatWrite
			if group == "hitting" {
				rows, err := sources.MLBHitting(season, "R")
				if err != nil {
					_ = database.MarkSeasonFailed(source, season, 0, historicalSeasonPipelineVersion)
					return SyncStepResult{}, fmt.Errorf("fetch historical MLB hitting: %w; prior rows were retained", err)
				}
				writes = hittingSyncWrites(rows)
			} else {
				rows, err := sources.MLBPitching(season, "R")
				if err != nil {
					_ = database.MarkSeasonFailed(source, season, 0, historicalSeasonPipelineVersion)
					return SyncStepResult{}, fmt.Errorf("fetch historical MLB pitching: %w; prior rows were retained", err)
				}
				writes = pitchingSyncWrites(rows, nil)
			}
			count := int64(len(writes))
			minimum := int64(200)
			if group == "pitching" {
				minimum = 150
			}
			if count < minimum {
				_ = database.MarkSeasonPartial(source, season, count, historicalSeasonPipelineVersion)
				return SyncStepResult{}, fmt.Errorf("historical MLB %s returned %d rows below the %d-row completeness minimum; prior rows were retained", group, count, minimum)
			}
			if err := database.ReplaceMLBSeasonStats(season, writes); err != nil {
				_ = database.MarkSeasonFailed(source, season, count, historicalSeasonPipelineVersion)
				return SyncStepResult{}, fmt.Errorf("persist historical MLB %s: %w; prior rows were retained", group, err)
			}
			if err := database.MarkSeasonComplete(source, season, count, historicalSeasonPipelineVersion); err != nil {
				return SyncStepResult{}, fmt.Errorf("complete historical MLB %s manifest: %w", group, err)
			}
			return SyncStepResult{Count: count}, nil
		},
	}
}

func mlbRosterStep(season int64, sources syncSourceSet) SyncStep {
	scope := strconv.FormatInt(season, 10)
	return SyncStep{Source: "mlb", Item: "40man_rosters", Scope: scope, Run: func(database *store.Store) (SyncStepResult, error) {
		teams, err := sources.MLBDirectory(season)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("fetch MLB roster directory: %w; all prior team rosters were retained", err)
		}
		if len(teams) != 30 {
			return SyncStepResult{}, fmt.Errorf("MLB roster directory requires 30 unique teams, received %d rows", len(teams))
		}
		unique := make(map[string]struct{}, len(teams))
		for _, team := range teams {
			identity := fmt.Sprintf("%d/%s", team.TeamID, strings.ToUpper(strings.TrimSpace(team.Abbreviation)))
			if team.TeamID <= 0 || strings.TrimSpace(team.Abbreviation) == "" {
				return SyncStepResult{}, fmt.Errorf("MLB roster directory requires 30 unique teams, received an incomplete identity")
			}
			unique[identity] = struct{}{}
		}
		if len(unique) != 30 {
			return SyncStepResult{}, fmt.Errorf("MLB roster directory requires 30 unique teams, received %d rows and %d unique identities", len(teams), len(unique))
		}
		succeeded, failed := int64(0), 0
		for _, team := range teams {
			teamScope := strings.ToUpper(strings.TrimSpace(team.Abbreviation))
			localID := team.TeamID
			policy := store.RowRefreshPolicy{TTL: 30 * time.Minute, Force: sources.Force, PipelineVersion: syncPipelineVersion}
			needs, stateErr := database.NeedsSyncRow("mlb", "40man_team", scope, "team", teamScope, policy)
			if stateErr != nil {
				failed++
				continue
			}
			if !needs {
				continue
			}
			if stateErr = database.MarkSyncRowAttempt("mlb", "40man_team", scope, "team", teamScope, &localID, syncPipelineVersion); stateErr != nil {
				failed++
				continue
			}
			rows, fetchErr := sources.MLBRoster(team.TeamID)
			if fetchErr == nil && len(rows) == 0 {
				fetchErr = fmt.Errorf("40-man roster is empty")
			}
			if fetchErr == nil {
				writes := make([]store.RosterWrite, 0, len(rows))
				for _, row := range rows {
					writes = append(writes, store.RosterWrite{MLBAMID: row.PersonID, Name: row.FullName, Position: row.Position, PrimaryType: row.PrimaryType, Status: row.Status, JerseyNumber: row.JerseyNumber})
				}
				fetchErr = database.ReplaceMLBRoster(teamScope, writes)
			}
			if fetchErr != nil {
				failed++
				_ = database.MarkSyncRowFailure("mlb", "40man_team", scope, "team", teamScope, &localID, syncPipelineVersion, fetchErr.Error())
				continue
			}
			if stateErr = database.MarkSyncRowSuccess("mlb", "40man_team", scope, "team", teamScope, &localID, syncPipelineVersion); stateErr != nil {
				failed++
				_ = database.MarkSyncRowFailure("mlb", "40man_team", scope, "team", teamScope, &localID, syncPipelineVersion, stateErr.Error())
				continue
			}
			succeeded++
		}
		if succeeded == 0 && failed > 0 {
			return SyncStepResult{}, fmt.Errorf("all MLB 40-man roster fetches failed; prior team rosters were retained")
		}
		result := SyncStepResult{Count: succeeded}
		if failed > 0 {
			result.Degraded = true
			result.Detail = fmt.Sprintf("%d teams succeeded, %d failed; prior failed-team rosters were retained", succeeded, failed)
		}
		return result, nil
	}}
}

func savantStep(season int64, group string, fetch func(int64) ([]providers.SavantRow, error)) SyncStep {
	return SyncStep{Source: "savant", Item: group, Scope: strconv.FormatInt(season, 10), Run: func(database *store.Store) (SyncStepResult, error) {
		rows, err := fetch(season)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("fetch Savant %s: %w; prior %s data was retained", group, err, group)
		}
		writes := make([]store.StatcastWrite, 0, len(rows))
		for _, row := range rows {
			writes = append(writes, store.StatcastWrite{MLBAMID: row.MLBAMID, Season: row.Season, StatGroup: row.StatGroup, PlateAppearances: row.PlateAppearances, BattedBallEvents: row.BattedBallEvents, XWOBA: row.XWOBA, ExitVeloAverage: row.ExitVeloAverage, BarrelPercent: row.BarrelPercent, HardHitPercent: row.HardHitPercent, SprintSpeed: row.SprintSpeed, StrikeoutPercent: row.StrikeoutPercent, WalkPercent: row.WalkPercent, OPS: row.OPS, FastballVelo: row.FastballVelo, WhiffPercent: row.WhiffPercent, ChasePercent: row.ChasePercent, GroundBallPercent: row.GroundBallPercent})
		}
		count, err := database.ReplaceStatcastSnapshot(season, group, writes)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("persist Savant %s: %w; prior %s data was retained", group, err, group)
		}
		return SyncStepResult{Count: int64(count)}, nil
	}}
}

func fanGraphsStep(season int64, sources syncSourceSet) SyncStep {
	return SyncStep{Source: "fangraphs", Item: "snapshot", Scope: strconv.FormatInt(season, 10), Run: func(database *store.Store) (SyncStepResult, error) {
		snapshot, err := sources.FanGraphs(season)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("fetch FanGraphs snapshot: %w; prior data was retained", err)
		}
		chart, err := sources.Closers()
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("fetch FanGraphs closer chart: %w; prior data was retained", err)
		}
		if err := providers.ValidateFanGraphsCloserCoverage(chart); err != nil {
			return SyncStepResult{}, fmt.Errorf("%w; prior data was retained", err)
		}
		projections := make([]store.ProjectionWrite, 0, len(snapshot.Projections))
		for _, row := range snapshot.Projections {
			projections = append(projections, store.ProjectionWrite{MLBAMID: row.MLBAMID, Season: row.Season, Source: row.Source, StatGroup: row.StatGroup, PA: row.PA, IP: row.IP, HR: row.HR, R: row.R, RBI: row.RBI, SB: row.SB, AVG: row.AVG, OBP: row.OBP, SLG: row.SLG, ERA: row.ERA, WHIP: row.WHIP, K: row.K, W: row.W, SV: row.SV, BB: row.BB})
		}
		projections, err = store.BlendProjections(projections)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("blend FanGraphs projections: %w; prior data was retained", err)
		}
		batted := make([]store.FanGraphsBattedBallWrite, 0, len(snapshot.BattedBall))
		for _, row := range snapshot.BattedBall {
			batted = append(batted, store.FanGraphsBattedBallWrite{MLBAMID: row.MLBAMID, Season: row.Season, FlyBallPct: row.FlyBallPct, HomeRunFB: row.HomeRunFB})
		}
		candidates := providers.FanGraphsCloserCandidates(chart)
		closers := make([]store.CloserWrite, 0, len(candidates))
		for _, row := range candidates {
			closers = append(closers, store.CloserWrite{Team: row.Team, Name: row.Name})
		}
		count, err := database.ReplaceFanGraphsSnapshot(season, projections, batted, closers)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("persist FanGraphs snapshot: %w; prior data was retained", err)
		}
		return SyncStepResult{Count: int64(count)}, nil
	}}
}

func fantasyProsStep(season int64, fetch func() ([]providers.ECRRow, error)) SyncStep {
	return SyncStep{Source: "fantasypros", Item: "ecr", Scope: strconv.FormatInt(season, 10), Run: func(database *store.Store) (SyncStepResult, error) {
		rows, err := fetch()
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("fetch FantasyPros ECR: %w; prior data was retained", err)
		}
		if err := providers.ValidateFantasyProsCompleteness(rows); err != nil {
			return SyncStepResult{}, fmt.Errorf("%w; prior data was retained", err)
		}
		writes := make([]store.ECRWrite, 0, len(rows))
		for _, row := range rows {
			writes = append(writes, store.ECRWrite{YahooPlayerID: row.YahooPlayerID, Name: row.Name, Team: row.Team, Rank: row.Rank})
		}
		count, err := database.ReplaceECR(writes)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("persist FantasyPros ECR: %w; prior data was retained", err)
		}
		return SyncStepResult{Count: int64(count)}, nil
	}}
}

func espnStep(now time.Time, fetch func(time.Time) (providers.ESPNSlateLines, error)) SyncStep {
	date := now.UTC().Format("2006-01-02")
	return SyncStep{Source: "espn", Item: "mlb_current_odds", Scope: date, Run: func(database *store.Store) (SyncStepResult, error) {
		slate, err := fetch(now)
		if err != nil {
			_, _ = database.MarkCommandSnapshotStale("mlb_current_odds", "espn", date, boundSyncDetail(err.Error(), 256))
			return SyncStepResult{}, fmt.Errorf("fetch ESPN odds: %w; prior odds snapshot was retained", err)
		}
		payload, err := json.Marshal(slate)
		if err != nil {
			return SyncStepResult{}, fmt.Errorf("serialize ESPN odds: %w", err)
		}
		if err := database.SaveCommandSnapshot("mlb_current_odds", "espn", date, "1", string(payload)); err != nil {
			_, _ = database.MarkCommandSnapshotStale("mlb_current_odds", "espn", date, boundSyncDetail(err.Error(), 256))
			return SyncStepResult{}, fmt.Errorf("persist ESPN odds: %w; prior odds snapshot was retained", err)
		}
		result := SyncStepResult{Count: int64(len(slate.Games))}
		if len(slate.Issues) > 0 {
			result.Degraded = true
			result.Detail = fmt.Sprintf("%d bounded odds issues", len(slate.Issues))
		}
		return result, nil
	}}
}

func hittingSyncWrites(rows []providers.BulkHittingSplit) []store.SeasonStatWrite {
	writes := make([]store.SeasonStatWrite, 0, len(rows))
	for _, row := range rows {
		stat := row.Stat
		writes = append(writes, store.SeasonStatWrite{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, TeamAbbreviation: mlbTeamAbbreviation(row.Team.TeamID), StatGroup: "hitting", Games: stat.GamesPlayed, PlateAppearances: stat.PlateAppearances, AtBats: stat.AtBats, Hits: stat.Hits, HomeRuns: stat.HomeRuns, RunsBattedIn: stat.RBI, Runs: stat.Runs, StolenBases: stat.StolenBases, Walks: stat.Walks, HitByPitch: stat.HitByPitch, TotalBases: stat.TotalBases, Strikeouts: stat.Strikeouts})
	}
	return writes
}

func pitchingSyncWrites(rows []providers.BulkPitchingSplit, qualityStarts map[int64]int64) []store.SeasonStatWrite {
	writes := make([]store.SeasonStatWrite, 0, len(rows))
	for _, row := range rows {
		stat := row.Stat
		quality := stat.QualityStarts
		if value, ok := qualityStarts[row.Player.PersonID]; ok {
			quality = value
		}
		writes = append(writes, store.SeasonStatWrite{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, TeamAbbreviation: mlbTeamAbbreviation(row.Team.TeamID), StatGroup: "pitching", Games: stat.GamesPitched, Wins: stat.Wins, Saves: stat.Saves, Holds: stat.Holds, Strikeouts: stat.Strikeouts, InningsOuts: inningsOuts(stat.InningsPitched), GamesStarted: stat.GamesStarted, QualityStarts: quality, HitsAllowed: stat.HitsAllowed, EarnedRuns: stat.EarnedRuns, PitcherWalks: stat.Walks})
	}
	return writes
}

func hittingIdentityCandidates(rows []providers.BulkHittingSplit) []store.IdentityCandidate {
	candidates := make([]store.IdentityCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, store.IdentityCandidate{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, Team: mlbTeamAbbreviation(row.Team.TeamID), Role: "B"})
	}
	return candidates
}

func pitchingIdentityCandidates(rows []providers.BulkPitchingSplit) []store.IdentityCandidate {
	candidates := make([]store.IdentityCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, store.IdentityCandidate{MLBAMID: row.Player.PersonID, Name: row.Player.FullName, Team: mlbTeamAbbreviation(row.Team.TeamID), Role: "P"})
	}
	return candidates
}

func mlbTeamAbbreviation(teamID int64) string {
	return map[int64]string{108: "LAA", 109: "ARI", 110: "BAL", 111: "BOS", 112: "CHC", 113: "CIN", 114: "CLE", 115: "COL", 116: "DET", 117: "HOU", 118: "KC", 119: "LAD", 120: "WSH", 121: "NYM", 133: "OAK", 134: "PIT", 135: "SD", 136: "SEA", 137: "SF", 138: "STL", 139: "TB", 140: "TEX", 141: "TOR", 142: "MIN", 143: "PHI", 144: "ATL", 145: "CWS", 146: "MIA", 147: "NYY", 158: "MIL"}[teamID]
}
