package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/queone/skout/internal/config"
	"github.com/queone/skout/internal/domain"
	"github.com/queone/skout/internal/providers"
	"github.com/queone/skout/internal/store"
)

const (
	syncPipelineVersion    = "provider-sync-v1"
	yahooSyncParallelLimit = 4
	terminalLineReset      = "\r\x1b[2K"
)

// SyncOptions contains production process evidence for foreground synchronization.
type SyncOptions struct {
	Version        string
	League         string
	Team           string
	Debug          bool
	Input          io.Reader
	Prompt         io.Writer
	Output         io.Writer
	InputTerminal  bool
	PromptTerminal bool
	OutputTerminal bool
}

// SyncStepResult is one successful provider-step result.
type SyncStepResult struct {
	Count    int64
	Degraded bool
	Detail   string
}

// SyncStep is one ordered, independently isolated provider refresh.
type SyncStep struct {
	Source string
	Item   string
	Scope  string
	Skip   func(*store.Store) (bool, error)
	Run    func(*store.Store) (SyncStepResult, error)
}

// SyncService contains injectable foreground synchronization boundaries.
type SyncService struct {
	Store            *store.Store
	Yahoo            providers.YahooFantasySource
	Steps            []SyncStep
	ConfigPath       string
	RuntimeDirectory string
	CanonicalLeague  func(string) (string, error)
	Input            io.Reader
	Prompt           io.Writer
	Output           io.Writer
	InputTerminal    bool
	OutputTerminal   bool
	Origin           store.SyncOrigin
	lockHeld         bool
	startReported    bool
	yahooProgress    *syncProgressLine
}

type syncOutcome struct {
	source, item string
	succeeded    bool
	skipped      bool
	degraded     bool
	count        int64
	detail       string
}

type matchupHistory struct {
	week     int
	matchups []domain.Matchup
	rosters  []domain.RosterWeekStats
}

type syncProgressLine struct {
	output           io.Writer
	terminal         bool
	prefix           string
	current          string
	players          int
	matchupsComplete int
	matchupsTotal    int
}

func newSyncProgressLine(output io.Writer, terminal bool, prefix string) *syncProgressLine {
	return &syncProgressLine{output: output, terminal: terminal, prefix: prefix, current: prefix}
}

func (line *syncProgressLine) reportPlayers(count int) {
	if count < line.players || count == line.players && line.current != line.prefix {
		return
	}
	line.players = count
	line.renderProgress()
}

func (line *syncProgressLine) reportMatchups(completed, total int) {
	if total <= 0 || completed < line.matchupsComplete || completed > total {
		return
	}
	if completed == line.matchupsComplete && total == line.matchupsTotal {
		return
	}
	line.matchupsComplete = completed
	line.matchupsTotal = total
	line.renderProgress()
}

func (line *syncProgressLine) renderProgress() {
	value := fmt.Sprintf("%s (%d)", line.prefix, line.players)
	if line.matchupsTotal > 0 {
		value = fmt.Sprintf("%s (%d; matchups %d/%d)", line.prefix, line.players, line.matchupsComplete, line.matchupsTotal)
	}
	line.current = value
	if line.terminal {
		_ = writeSyncOutput(line.output, terminalLineReset+value)
	}
}

func (line *syncProgressLine) suspend() error {
	if !line.terminal {
		return nil
	}
	return writeSyncOutput(line.output, terminalLineReset)
}

func (line *syncProgressLine) resume() error {
	if !line.terminal {
		return nil
	}
	return writeSyncOutput(line.output, terminalLineReset+line.current)
}

func (line *syncProgressLine) finish(suffix string) error {
	if line.terminal {
		return writeSyncOutput(line.output, terminalLineReset+line.prefix+suffix+"\n")
	}
	return writeSyncOutput(line.output, suffix+"\n")
}

// Run executes one ordered foreground synchronization.
func (service *SyncService) Run(leagueOverride, teamOverride string) (string, error) {
	if service.Store == nil || service.Yahoo == nil || service.ConfigPath == "" {
		return "", fmt.Errorf("sync: runtime boundaries are incomplete; reinstall skout")
	}
	if service.Input == nil {
		service.Input = strings.NewReader("")
	}
	if service.Prompt == nil {
		service.Prompt = io.Discard
	}
	if service.Output == nil {
		service.Output = io.Discard
	}
	if service.Origin == "" {
		service.Origin = store.OriginManual
	}
	if !service.startReported {
		if err := writeSyncOutput(service.Output, "==> Sync started.\n"); err != nil {
			return "", fmt.Errorf("sync: write progress: %w", err)
		}
	}
	if !service.lockHeld {
		runtimeDirectory := service.RuntimeDirectory
		if runtimeDirectory == "" {
			runtimeDirectory = filepath.Join(filepath.Dir(service.ConfigPath), "runtime")
		}
		guard, err := acquireSyncGuard(runtimeDirectory)
		if err != nil {
			return "", err
		}
		defer guard.release()
	}
	settings, err := config.ReadAt(service.ConfigPath)
	if err != nil {
		return "", fmt.Errorf("sync: read configuration: %w", err)
	}
	requestedLeague := strings.TrimSpace(leagueOverride)
	if requestedLeague == "" {
		requestedLeague = strings.TrimSpace(settings.CurrentLeague)
	}
	if requestedLeague == "" {
		requestedLeague = strings.TrimSpace(settings.PullPublicLeagueID)
	}
	if requestedLeague == "" {
		if !service.InputTerminal {
			return "", fmt.Errorf("sync: no Yahoo league configured; run skout sync -l <league-id-or-key> -T <team> and retry")
		}
		if err := writeSyncOutput(service.Prompt, "Yahoo league id or key: "); err != nil {
			return "", fmt.Errorf("sync: write league prompt: %w", err)
		}
		line, readErr := bufio.NewReader(service.Input).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", fmt.Errorf("sync: read league prompt: %w", readErr)
		}
		requestedLeague = strings.TrimSpace(line)
	}
	canonical := service.CanonicalLeague
	if canonical == nil {
		canonical = func(value string) (string, error) {
			value = strings.TrimSpace(value)
			if value == "" {
				return "", fmt.Errorf("league key is empty")
			}
			return value, nil
		}
	}
	leagueKey, err := canonical(requestedLeague)
	if err != nil {
		return "", fmt.Errorf("sync: resolve public Yahoo league: %w", err)
	}
	runID, err := service.Store.StartSyncRun(store.SyncLive, service.Origin)
	if err != nil {
		return "", fmt.Errorf("sync: start sync run: %w", err)
	}
	outcomes := make([]syncOutcome, 0, len(service.Steps)+1)
	yahoo := service.runSyncItem("yahoo_public", "fantasy", leagueKey, func(database *store.Store) (SyncStepResult, error) {
		teamKey, count, err := service.synchronizeYahoo(database, leagueKey, teamOverride, settings.CurrentTeamKey)
		if err != nil {
			return SyncStepResult{}, err
		}
		updated := settings
		updated.CurrentLeague = leagueKey
		updated.CurrentTeamKey = teamKey
		updated.PullPublicLeagueID = ""
		if err := config.WriteAt(service.ConfigPath, updated); err != nil {
			return SyncStepResult{}, fmt.Errorf("save team identity: %w", err)
		}
		settings = updated
		return SyncStepResult{Count: count}, nil
	})
	outcomes = append(outcomes, yahoo)
	for _, step := range service.Steps {
		if step.Run == nil || strings.TrimSpace(step.Source) == "" || strings.TrimSpace(step.Item) == "" {
			outcomes = append(outcomes, syncOutcome{source: step.Source, item: step.Item, detail: "step boundary is incomplete"})
			continue
		}
		if step.Skip != nil {
			skip, skipErr := step.Skip(service.Store)
			if skipErr != nil {
				_ = writeSyncOutput(service.Output, fmt.Sprintf("==> %s %s: fetching", step.Source, step.Item))
				outcomes = append(outcomes, service.finishOutcome(syncOutcome{source: step.Source, item: step.Item, detail: "evaluate completeness: " + skipErr.Error()}))
				continue
			}
			if skip {
				_ = writeSyncOutput(service.Output, fmt.Sprintf("==> %s %s: fetching", step.Source, step.Item))
				outcomes = append(outcomes, service.finishOutcome(syncOutcome{source: step.Source, item: step.Item, succeeded: true, skipped: true}))
				continue
			}
		}
		outcomes = append(outcomes, service.runSyncItem(step.Source, step.Item, step.Scope, step.Run))
	}
	counts := make(map[string]int64, len(outcomes))
	successes, failures, degradations := 0, 0, 0
	var failureDetails []string
	for _, outcome := range outcomes {
		counts[outcome.source+"_"+outcome.item] = outcome.count
		if outcome.succeeded {
			successes++
			if outcome.degraded {
				degradations++
			}
		} else if !outcome.skipped {
			failures++
			if outcome.detail != "" {
				failureDetails = append(failureDetails, outcome.source+" "+outcome.item+": "+outcome.detail)
			}
		}
	}
	if successes == 0 {
		_, _ = service.Store.FailSyncRun(runID)
		detail := strings.Join(failureDetails, "; ")
		_ = service.Store.RecordProviderFailure(detail)
		return "", fmt.Errorf("sync failed: every provider failed\nCheck network access and provider availability, then retry")
	}
	changed, err := service.Store.CompleteSyncRun(runID, counts)
	if err != nil {
		return "", fmt.Errorf("sync: complete sync run: %w", err)
	}
	if !changed {
		return "", fmt.Errorf("sync: complete sync run: run %d was not active", runID)
	}
	degraded := failures > 0 || degradations > 0
	if err := service.Store.RecordProviderSuccess(degraded); err != nil {
		return "", fmt.Errorf("sync: update dashboard: %w", err)
	}
	disposition := "success"
	if degraded {
		disposition = "degraded success"
	}
	return fmt.Sprintf("==> Sync %s: %d steps succeeded, %d failed.\n", disposition, successes, failures), nil
}

func (service *SyncService) runSyncItem(source, item, scope string, action func(*store.Store) (SyncStepResult, error)) syncOutcome {
	prefix := fmt.Sprintf("==> %s %s: fetching", source, item)
	if source == "yahoo_public" && item == "fantasy" {
		service.yahooProgress = newSyncProgressLine(service.Output, service.OutputTerminal, prefix)
	}
	if err := writeSyncOutput(service.Output, prefix); err != nil {
		service.yahooProgress = nil
		return syncOutcome{source: source, item: item, detail: "write progress: " + err.Error()}
	}
	policy := store.ItemRefreshPolicy{TTL: 30 * time.Minute, Force: service.Origin == store.OriginManual, PipelineVersion: syncPipelineVersion}
	needs, err := service.Store.NeedsSyncItem(source, item, scope, policy)
	if err != nil {
		return service.finishOutcome(syncOutcome{source: source, item: item, detail: err.Error()})
	}
	if !needs {
		return service.finishOutcome(syncOutcome{source: source, item: item, succeeded: true, skipped: true})
	}
	if err := service.Store.MarkSyncItemAttempt(source, item, scope, syncPipelineVersion); err != nil {
		return service.finishOutcome(syncOutcome{source: source, item: item, detail: err.Error()})
	}
	result, runErr := action(service.Store)
	if runErr != nil {
		detail := boundSyncDetail(runErr.Error(), 256)
		_ = service.Store.MarkSyncItemFailure(source, item, scope, syncPipelineVersion, detail)
		return service.finishOutcome(syncOutcome{source: source, item: item, detail: detail})
	}
	outcome := syncOutcome{source: source, item: item, succeeded: true, degraded: result.Degraded, count: result.Count, detail: boundSyncDetail(result.Detail, 256)}
	if result.Degraded {
		err = service.Store.MarkSyncItemDegraded(source, item, scope, syncPipelineVersion, outcome.detail)
	} else {
		err = service.Store.MarkSyncItemSuccess(source, item, scope, syncPipelineVersion)
	}
	if err != nil {
		outcome.succeeded = false
		outcome.degraded = false
		outcome.detail = err.Error()
		_ = service.Store.MarkSyncItemFailure(source, item, scope, syncPipelineVersion, outcome.detail)
	}
	return service.finishOutcome(outcome)
}

func (service *SyncService) finishOutcome(outcome syncOutcome) syncOutcome {
	status := "failed"
	switch {
	case outcome.skipped:
		status = "fresh"
	case outcome.succeeded && outcome.degraded:
		status = "degraded"
	case outcome.succeeded:
		status = "success"
	}
	line := " -> " + status
	if outcome.detail != "" {
		line += ": " + outcome.detail
	} else {
		line += " (" + strconv.FormatInt(outcome.count, 10) + ")"
	}
	if service.yahooProgress != nil && outcome.source == "yahoo_public" && outcome.item == "fantasy" {
		_ = service.yahooProgress.finish(line)
		service.yahooProgress = nil
	} else {
		_ = writeSyncOutput(service.Output, line+"\n")
	}
	return outcome
}

func (service *SyncService) reportYahooPlayers(count int) {
	if service.yahooProgress != nil {
		service.yahooProgress.reportPlayers(count)
	}
}

func (service *SyncService) reportYahooMatchups(completed, total int) {
	if service.yahooProgress != nil {
		service.yahooProgress.reportMatchups(completed, total)
	}
}

func (service *SyncService) suspendYahooProgress() error {
	if service.yahooProgress == nil {
		return nil
	}
	return service.yahooProgress.suspend()
}

func (service *SyncService) resumeYahooProgress() error {
	if service.yahooProgress == nil {
		return nil
	}
	return service.yahooProgress.resume()
}

func (service *SyncService) synchronizeYahoo(database *store.Store, leagueKey, teamOverride, savedTeam string) (string, int64, error) {
	settings, err := service.Yahoo.LeagueSettings(leagueKey)
	if err != nil {
		return "", 0, fmt.Errorf("fetch public Yahoo settings: %w", err)
	}
	teams, err := service.Yahoo.Standings(leagueKey)
	if err != nil {
		return "", 0, fmt.Errorf("fetch public Yahoo standings: %w", err)
	}
	teamKeys := make([]string, 0, len(teams))
	for _, team := range teams {
		teamKeys = append(teamKeys, team.TeamKey)
	}
	rosters, err := service.Yahoo.LeagueRosters(leagueKey, teamKeys, func(count int) {
		service.reportYahooPlayers(count)
	})
	if err != nil {
		return "", 0, fmt.Errorf("fetch public Yahoo rosters: %w", err)
	}
	service.reportYahooPlayers(len(rosters.Players))
	freeAgents, err := service.Yahoo.FreeAgents(leagueKey, func(count int) {
		service.reportYahooPlayers(len(rosters.Players) + count)
	})
	if err != nil {
		return "", 0, fmt.Errorf("fetch public Yahoo free agents: %w", err)
	}
	service.reportYahooPlayers(len(rosters.Players) + len(freeAgents))
	requestedTeam := strings.TrimSpace(teamOverride)
	if requestedTeam == "" {
		requestedTeam = strings.TrimSpace(savedTeam)
	}
	teamKey, err := selectPrimaryTeam(teams, requestedTeam, service.InputTerminal, service.Input, service.Prompt, service.suspendYahooProgress, service.resumeYahooProgress)
	if err != nil {
		return "", 0, err
	}
	if settings.CurrentWeek != nil && *settings.CurrentWeek > 0 {
		service.reportYahooMatchups(0, *settings.CurrentWeek)
	}
	history, err := acquireMatchupHistory(service.Yahoo, leagueKey, teamKey, settings.CurrentWeek, service.reportYahooMatchups)
	if err != nil {
		return "", 0, err
	}
	players := append(append([]domain.FantasyPlayer(nil), rosters.Players...), freeAgents...)
	categories := make([]store.CategoryWrite, 0, len(settings.Categories))
	for _, category := range settings.Categories {
		categories = append(categories, store.CategoryWrite{StatID: category.StatID, Abbreviation: category.Abbreviation, Name: category.Name, SortOrder: category.SortOrder, DisplayOnly: category.DisplayOnly, Sequence: category.Sequence})
	}
	positions := make([]store.PositionWrite, 0, len(settings.RosterPositions))
	for _, position := range settings.RosterPositions {
		positions = append(positions, store.PositionWrite{Position: position.Position.String(), Count: position.Count})
	}
	commands := make([]store.FantasyCommandSnapshotWrite, 0, len(history)*3)
	for _, entry := range history {
		payload, err := json.Marshal(entry.matchups)
		if err != nil {
			return "", 0, fmt.Errorf("serialize matchup history: %w", err)
		}
		commands = append(commands, store.FantasyCommandSnapshotWrite{Dataset: "match_scoreboard", Source: "yahoo", Scope: fmt.Sprintf("%s:%d", leagueKey, entry.week), Version: "v1", Payload: string(payload)})
		for _, roster := range entry.rosters {
			payload, err := json.Marshal(roster)
			if err != nil {
				return "", 0, fmt.Errorf("serialize matchup roster history: %w", err)
			}
			commands = append(commands, store.FantasyCommandSnapshotWrite{Dataset: "match_roster", Source: "yahoo", Scope: fmt.Sprintf("%s:%d", roster.TeamKey, entry.week), Version: "v1", Payload: string(payload)})
		}
	}
	if err := database.ReplaceFantasySyncSnapshot(store.FantasySnapshotWrite{League: settings.League, CurrentWeek: settings.CurrentWeek, Categories: categories, Positions: positions, Teams: teams, Players: players, Slots: rosters.Slots}, commands); err != nil {
		return "", 0, fmt.Errorf("persist public Yahoo fantasy snapshot: %w; prior complete data was retained", err)
	}
	return teamKey, int64(len(players)), nil
}

// SelectPrimaryTeam resolves exact or unique case-insensitive team matches.
func SelectPrimaryTeam(teams []domain.FantasyTeam, requested string, interactive bool, input io.Reader, prompt io.Writer) (string, error) {
	return selectPrimaryTeam(teams, requested, interactive, input, prompt, nil, nil)
}

func selectPrimaryTeam(teams []domain.FantasyTeam, requested string, interactive bool, input io.Reader, prompt io.Writer, beforePrompt, afterPrompt func() error) (string, error) {
	query := strings.TrimSpace(requested)
	if query != "" {
		for _, team := range teams {
			if team.TeamKey == query {
				return team.TeamKey, nil
			}
		}
		var exact, partial []domain.FantasyTeam
		for _, team := range teams {
			if strings.EqualFold(team.Name, query) {
				exact = append(exact, team)
			}
			if strings.Contains(strings.ToLower(team.Name), strings.ToLower(query)) {
				partial = append(partial, team)
			}
		}
		if len(exact) == 1 {
			return exact[0].TeamKey, nil
		}
		if len(partial) == 1 {
			return partial[0].TeamKey, nil
		}
		if !interactive {
			if len(partial) == 0 {
				return "", fmt.Errorf("select primary team: no team matches %q; run skout sync -T <key-or-name> and retry", query)
			}
			return "", fmt.Errorf("select primary team: %q is ambiguous; use an exact team key or name", query)
		}
	}
	if len(teams) == 0 {
		return "", fmt.Errorf("select primary team: the public league contains no teams; verify the league id and retry")
	}
	if len(teams) == 1 {
		return teams[0].TeamKey, nil
	}
	if !interactive {
		return "", fmt.Errorf("select primary team: run skout sync -T <key-or-name> and retry")
	}
	if input == nil {
		input = strings.NewReader("")
	}
	if prompt == nil {
		prompt = io.Discard
	}
	if beforePrompt != nil {
		if err := beforePrompt(); err != nil {
			return "", fmt.Errorf("select primary team: suspend progress: %w", err)
		}
	}
	if _, err := io.WriteString(prompt, "Select your primary team:\n"); err != nil {
		return "", fmt.Errorf("select primary team: write selection: %w", err)
	}
	for index, team := range teams {
		if _, err := fmt.Fprintf(prompt, "  %d. %s  %s\n", index+1, team.TeamKey, team.Name); err != nil {
			return "", fmt.Errorf("select primary team: write selection: %w", err)
		}
	}
	if err := writeSyncOutput(prompt, "Choice: "); err != nil {
		return "", fmt.Errorf("select primary team: write selection: %w", err)
	}
	line, readErr := bufio.NewReader(input).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("select primary team: read selection: %w", readErr)
	}
	selected, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if parseErr != nil || selected < 1 || selected > len(teams) {
		return "", fmt.Errorf("select primary team: enter one of the displayed numbers and retry")
	}
	if afterPrompt != nil {
		if err := afterPrompt(); err != nil {
			return "", fmt.Errorf("select primary team: redraw progress: %w", err)
		}
	}
	return teams[selected-1].TeamKey, nil
}

func acquireMatchupHistory(source providers.YahooFantasySource, leagueKey, teamKey string, currentWeek *int, progress func(completed, total int)) ([]matchupHistory, error) {
	if currentWeek == nil || *currentWeek <= 0 {
		return nil, nil
	}
	type historyResult struct {
		week  int
		entry matchupHistory
		err   error
	}
	workerCount := min(yahooSyncParallelLimit, *currentWeek)
	jobs := make(chan int, workerCount)
	results := make(chan historyResult, workerCount)
	var workers sync.WaitGroup
	for range workerCount {
		workers.Go(func() {
			for week := range jobs {
				entry, err := fetchMatchupHistoryWeek(source, leagueKey, teamKey, week)
				results <- historyResult{week: week, entry: entry, err: err}
			}
		})
	}
	history := make([]matchupHistory, *currentWeek)
	next, outstanding := 1, 0
	for outstanding < workerCount && next <= *currentWeek {
		jobs <- next
		next++
		outstanding++
	}
	completed := 0
	var firstErr error
	for outstanding > 0 {
		result := <-results
		outstanding--
		if firstErr == nil {
			if result.err != nil {
				firstErr = result.err
			} else {
				history[result.week-1] = result.entry
				completed++
				if progress != nil {
					progress(completed, *currentWeek)
				}
				if next <= *currentWeek {
					jobs <- next
					next++
					outstanding++
				}
			}
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return history, nil
}

func fetchMatchupHistoryWeek(source providers.YahooFantasySource, leagueKey, teamKey string, week int) (matchupHistory, error) {
	selectedWeek := week
	matchups, err := source.Scoreboard(leagueKey, &selectedWeek)
	if err != nil {
		return matchupHistory{}, fmt.Errorf("sync matchup history: %w", err)
	}
	entry := matchupHistory{week: week, matchups: matchups}
	for _, matchup := range matchups {
		if matchup.Teams[0].TeamKey != teamKey && matchup.Teams[1].TeamKey != teamKey {
			continue
		}
		for _, team := range matchup.Teams {
			roster, rosterErr := source.RosterWeekStats(team.TeamKey, week)
			if rosterErr != nil {
				return matchupHistory{}, fmt.Errorf("sync matchup roster history: %w", rosterErr)
			}
			entry.rosters = append(entry.rosters, roster)
		}
		break
	}
	return entry, nil
}

type syncGuard struct{ file *os.File }

func acquireSyncGuard(directory string) (*syncGuard, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("sync: resolve synchronization runtime: directory is empty")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("sync: create synchronization runtime: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("sync: secure synchronization runtime: %w", err)
	}
	path := filepath.Join(directory, "sync.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sync: open synchronization lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync: secure synchronization lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync: start synchronization: another synchronization is running")
	}
	return &syncGuard{file: file}, nil
}

func (guard *syncGuard) release() {
	if guard == nil || guard.file == nil {
		return
	}
	_ = syscall.Flock(int(guard.file.Fd()), syscall.LOCK_UN)
	_ = guard.file.Close()
}

func writeSyncOutput(output io.Writer, value string) error {
	if _, err := io.WriteString(output, value); err != nil {
		return err
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func boundSyncDetail(value string, maximum int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maximum {
		return string(runes)
	}
	return string(runes[:maximum])
}
