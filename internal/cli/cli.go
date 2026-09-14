// Package cli owns skout's descriptor-driven grammar, help, and dispatch.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/queone/skout/internal/app"
	"github.com/queone/skout/internal/glossary"
	"github.com/queone/skout/internal/terminal"
)

// Context contains all process evidence used by CLI behavior.
type Context struct {
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	Prompt           io.Writer
	StdinIsTerminal  bool
	StdoutIsTerminal bool
	StderrIsTerminal bool
	NoColor          string
	Term             string
	ColorTerm        string
}

// Handlers contains injectable executable application boundaries.
type Handlers struct {
	Fetch        func(host, path string) (string, error)
	Status       func(league string) (string, error)
	Sync         func(league, team string, debug bool, output io.Writer) (string, error)
	Reset        func(input io.Reader, output io.Writer) error
	Matchup      func(league, team string, week int, weekly bool, day string, season int, debug bool) (string, error)
	Matchups     func(league string, week int, season int, debug bool) (string, error)
	Teams        func(team string, force, debug bool) (string, error)
	Totals       func(force, debug bool) (string, error)
	Probables    func(force, debug bool) (string, error)
	Roster       func(league, team string, season int, debug bool) (string, error)
	RosterTotals func(league, weekly string, season int, debug bool) (string, error)
	Hitters      func(league, argument, sort, position string, waiver bool, season int, debug bool) (string, error)
	Pitchers     func(league, argument, sort, position string, waiver bool, season int, debug bool) (string, error)
}

type flagSpec struct {
	short, long, value, description string
	optional                        bool
}
type commandSpec struct {
	name, label, description, positional string
	minimum, maximum                     int
	aliases                              []string
	flags                                []flagSpec
}

var globalFlags = []flagSpec{{"-l", "--league", "KEY", "Yahoo league key", false}, {"-d", "--debug", "", "Print operation diagnostics", false}}

var commands = []commandSpec{
	{name: "fetch", label: "fetch <HOST> <PATH>", description: "Fetch a raw provider path for debugging", positional: "HOST PATH", minimum: 2, maximum: 2},
	{name: "st", label: "st", description: "Show status"},
	{name: "sync", label: "sync", description: "Synchronize the selected league", flags: []flagSpec{{"-T", "--team", "TEAM", "Select the primary fantasy team", false}}},
	{name: "reset", label: "reset", description: "Delete the local skout database"},
	{name: "m", label: "m [TEAM]", description: "Show a daily or weekly matchup", positional: "NAME", maximum: 1, flags: []flagSpec{{"-w", "--week", "WEEK", "Show a specific matchup week", false}, {"-W", "--weekly", "", "Show weekly running totals", false}, {"-D", "--day", "MMM-DD", "Show stats for a specific day", false}, seasonFlag()}},
	{name: "mm", label: "mm", description: "Show every league matchup for the week", flags: []flagSpec{{"-w", "--week", "WEEK", "Show a specific matchup week", false}, seasonFlag()}},
	{name: "t", label: "t [TEAM]", description: "Show MLB 40-man rosters", positional: "TEAM", maximum: 1, flags: []flagSpec{{"-f", "--force", "", "Refresh provider data", false}}},
	{name: "tt", label: "tt", description: "Show MLB standings and team totals", flags: []flagSpec{{"-f", "--force", "", "Refresh provider data", false}}},
	{name: "sp", label: "sp", description: "Show the three-day probable-pitcher slate", flags: []flagSpec{{"-f", "--force", "", "Refresh provider data", false}}},
	{name: "r", label: "r [NAME]", description: "Show a fantasy roster", positional: "NAME", maximum: 1, flags: []flagSpec{seasonFlag()}},
	{name: "rt", label: "rt", description: "Show fantasy roster totals", flags: []flagSpec{{"-w", "--weekly", "WEEK|DATE", "Show current or selected weekly totals", true}, seasonFlag()}},
	{name: "h", label: "h [N|NAME]", description: "Browse hitters or show a player", positional: "N|NAME", maximum: 1, flags: playerFlags()},
	{name: "p", label: "p [N|NAME]", description: "Browse pitchers or show a player", positional: "N|NAME", maximum: 1, flags: playerFlags()},
	{name: "i", label: "i [TERM]", description: "Look up a term in the skout glossary", positional: "TERM", maximum: 1, aliases: []string{"whatis"}},
}

func playerFlags() []flagSpec {
	return []flagSpec{{"-s", "--sort", "FIELD", "Sort by a displayed field", false}, {"-p", "--position", "POS", "Filter by eligible position", false}, {"-w", "--waiver", "", "Show available Yahoo pickup players", false}, seasonFlag()}
}

func seasonFlag() flagSpec {
	return flagSpec{"-S", "--season", "SEASON", "Show an archived season", false}
}

// ProductionHandlers returns application handlers using only public providers.
func ProductionHandlers(version string, context Context) Handlers {
	mode := colorMode(context)
	withMLB := func(debug bool, run func(*app.MLBService) (string, error)) (string, error) {
		service, err := app.NewProductionMLBService(version, context.Stdin, context.Prompt, context.StdinIsTerminal, context.StderrIsTerminal, mode)
		if err != nil {
			return "", err
		}
		defer service.Close()
		service.Debug = debug
		service.DebugOutput = context.Stderr
		return run(service)
	}
	autoSync := func(league string) func() error {
		return func() error {
			_, _ = io.WriteString(context.Stderr, "==> Yahoo data is older than 6 hours; syncing first.\n")
			_, err := app.SyncProduction(app.SyncOptions{
				Version: version, League: league, Auto: true,
				Input: context.Stdin, Prompt: context.Prompt, Output: context.Stderr,
				InputTerminal: context.StdinIsTerminal, PromptTerminal: context.StderrIsTerminal, OutputTerminal: context.StderrIsTerminal,
			})
			return err
		}
	}
	withFantasy := func(league string, season int, run func(*app.FantasyService) (string, error)) (string, error) {
		service, err := app.NewProductionFantasyService(version, league, season, mode)
		if err != nil {
			return "", err
		}
		defer service.Close()
		service.AutoSync = autoSync(league)
		return run(service)
	}
	withMatchup := func(league string, season int, run func(*app.MatchupService) (string, error)) (string, error) {
		service, err := app.NewProductionMatchupService(version, league, season, mode)
		if err != nil {
			return "", err
		}
		defer service.Close()
		service.AutoSync = autoSync(league)
		return run(service)
	}
	return Handlers{
		Fetch:  func(host, path string) (string, error) { return app.FetchProduction(version, host, path) },
		Status: func(league string) (string, error) { return app.StatusProduction(league, mode) },
		Sync: func(league, team string, debug bool, output io.Writer) (string, error) {
			return app.SyncProduction(app.SyncOptions{
				Version: version, League: league, Team: team, Debug: debug,
				Input: context.Stdin, Prompt: context.Prompt, Output: output,
				InputTerminal: context.StdinIsTerminal, PromptTerminal: context.StderrIsTerminal, OutputTerminal: context.StdoutIsTerminal,
			})
		},
		Reset: func(input io.Reader, output io.Writer) error {
			return app.ResetProduction(input, output, mode)
		},
		Matchup: func(league, team string, week int, weekly bool, day string, season int, _ bool) (string, error) {
			return withMatchup(league, season, func(service *app.MatchupService) (string, error) {
				return service.Matchup(app.MatchupOptions{Team: team, Week: week, Weekly: weekly, Day: day})
			})
		},
		Matchups: func(league string, week int, season int, _ bool) (string, error) {
			return withMatchup(league, season, func(service *app.MatchupService) (string, error) {
				return service.LeagueMatchups(app.LeagueMatchupsOptions{Week: week})
			})
		},
		Teams: func(team string, force, debug bool) (string, error) {
			return withMLB(debug, func(service *app.MLBService) (string, error) { return service.Teams(team, force) })
		},
		Totals: func(force, debug bool) (string, error) {
			return withMLB(debug, func(service *app.MLBService) (string, error) { return service.Totals(force) })
		},
		Probables: func(force, debug bool) (string, error) {
			return withMLB(debug, func(service *app.MLBService) (string, error) { return service.Probables(force) })
		},
		Roster: func(league, team string, season int, _ bool) (string, error) {
			return withFantasy(league, season, func(service *app.FantasyService) (string, error) { return service.Roster(team) })
		},
		RosterTotals: func(league, weekly string, season int, _ bool) (string, error) {
			return withFantasy(league, season, func(service *app.FantasyService) (string, error) { return service.Totals(weekly) })
		},
		Hitters: func(league, argument, sort, position string, waiver bool, season int, _ bool) (string, error) {
			return withFantasy(league, season, func(service *app.FantasyService) (string, error) {
				return service.Pool("B", app.PlayerPoolOptions{Argument: argument, Sort: sort, Position: position, Waiver: waiver})
			})
		},
		Pitchers: func(league, argument, sort, position string, waiver bool, season int, _ bool) (string, error) {
			return withFantasy(league, season, func(service *app.FantasyService) (string, error) {
				return service.Pool("P", app.PlayerPoolOptions{Argument: argument, Sort: sort, Position: position, Waiver: waiver})
			})
		},
	}
}

type parsedInvocation struct {
	spec                            commandSpec
	values                          map[string]string
	booleans                        map[string]bool
	positionals                     []string
	league                          string
	leagueSet, debug, help, version bool
}

// Run parses and dispatches one argument vector without exiting the process.
func Run(args []string, version string, context Context, handlers Handlers) int {
	if context.Stdout == nil {
		context.Stdout = io.Discard
	}
	if context.Stderr == nil {
		context.Stderr = io.Discard
	}
	if context.Stdin == nil {
		context.Stdin = strings.NewReader("")
	}
	if context.Prompt == nil {
		context.Prompt = context.Stderr
	}
	if len(args) == 0 {
		_, _ = io.WriteString(context.Stdout, RootHelp(version, colorMode(context)))
		return 0
	}
	switch rootAction(args) {
	case "help":
		_, _ = io.WriteString(context.Stdout, RootHelp(version, colorMode(context)))
		return 0
	case "version":
		fmt.Fprintf(context.Stdout, "skout v%s\n", version)
		return 0
	}
	if parsed, diagnostic, handled := parseRootOnly(args); handled {
		if diagnostic != "" {
			_, _ = io.WriteString(context.Stderr, diagnostic)
			return 2
		}
		if parsed.debug {
			source := "saved"
			if parsed.leagueSet {
				source = "override"
			}
			fmt.Fprintf(context.Stderr, "skout debug: command=root league_source=%s\n", source)
		}
		return 0
	}
	parsed, diagnostic := parse(args)
	if diagnostic != "" {
		_, _ = io.WriteString(context.Stderr, diagnostic)
		return 2
	}
	if parsed.help {
		_, _ = io.WriteString(context.Stdout, CommandHelp(parsed.spec, version, colorMode(context)))
		return 0
	}
	if parsed.version {
		fmt.Fprintf(context.Stdout, "skout v%s\n", version)
		return 0
	}
	if parsed.debug {
		source := "saved"
		if parsed.leagueSet {
			source = "override"
		}
		fmt.Fprintf(context.Stderr, "skout debug: command=%s league_source=%s\n", parsed.spec.name, source)
	}
	if parsed.spec.name == "i" {
		return runGlossary(parsed, context)
	}
	var output string
	var err error
	switch parsed.spec.name {
	case "fetch":
		fmt.Fprintf(context.Stderr, "skout fetch: %s\n", parsed.positionals[0])
		if handlers.Fetch == nil {
			err = fmt.Errorf("fetch: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Fetch(parsed.positionals[0], parsed.positionals[1])
		}
	case "st":
		if handlers.Status == nil {
			err = fmt.Errorf("status: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Status(parsed.league)
		}
	case "sync":
		if handlers.Sync == nil {
			err = fmt.Errorf("sync: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Sync(parsed.league, parsed.values["team"], parsed.debug, context.Stdout)
		}
	case "reset":
		if handlers.Reset == nil {
			err = fmt.Errorf("reset: runtime is unavailable; reinstall skout")
		} else {
			err = handlers.Reset(context.Stdin, context.Stdout)
		}
	case "m":
		team := firstPositional(parsed.positionals)
		week := 0
		if parsed.values["week"] != "" {
			week, _ = strconv.Atoi(parsed.values["week"])
		}
		if handlers.Matchup == nil {
			err = fmt.Errorf("match: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Matchup(parsed.league, team, week, parsed.booleans["weekly"], parsed.values["day"], seasonValue(parsed), parsed.debug)
		}
	case "mm":
		week := 0
		if parsed.values["week"] != "" {
			week, _ = strconv.Atoi(parsed.values["week"])
		}
		if handlers.Matchups == nil {
			err = fmt.Errorf("matchups: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Matchups(parsed.league, week, seasonValue(parsed), parsed.debug)
		}
	case "t":
		team := ""
		if len(parsed.positionals) > 0 {
			team = parsed.positionals[0]
		}
		if handlers.Teams == nil {
			err = fmt.Errorf("team: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Teams(team, parsed.booleans["force"], parsed.debug)
		}
	case "tt":
		if handlers.Totals == nil {
			err = fmt.Errorf("team totals: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Totals(parsed.booleans["force"], parsed.debug)
		}
	case "sp":
		if handlers.Probables == nil {
			err = fmt.Errorf("probable pitchers: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Probables(parsed.booleans["force"], parsed.debug)
		}
	case "r":
		if handlers.Roster == nil {
			err = fmt.Errorf("roster: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.Roster(parsed.league, firstPositional(parsed.positionals), seasonValue(parsed), parsed.debug)
		}
	case "rt":
		if handlers.RosterTotals == nil {
			err = fmt.Errorf("roster totals: runtime is unavailable; reinstall skout")
		} else {
			output, err = handlers.RosterTotals(parsed.league, parsed.values["weekly"], seasonValue(parsed), parsed.debug)
		}
	case "h", "p":
		handler := handlers.Hitters
		if parsed.spec.name == "p" {
			handler = handlers.Pitchers
		}
		if handler == nil {
			err = fmt.Errorf("player: runtime is unavailable; reinstall skout")
		} else {
			output, err = handler(parsed.league, firstPositional(parsed.positionals), parsed.values["sort"], parsed.values["position"], parsed.booleans["waiver"], seasonValue(parsed), parsed.debug)
		}
	}
	if err != nil {
		fmt.Fprintln(context.Stderr, err)
		return 1
	}
	if parsed.spec.name == "reset" {
		return 0
	}
	_, _ = io.WriteString(context.Stdout, output)
	return 0
}

func parse(args []string) (parsedInvocation, string) {
	commandIndex := -1
	var spec commandSpec
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-l" || argument == "--league" {
			index++
			continue
		}
		if strings.HasPrefix(argument, "--league=") || strings.HasPrefix(argument, "-l") && len(argument) > 2 || argument == "-d" || argument == "--debug" {
			continue
		}
		if strings.HasPrefix(argument, "-") {
			return parsedInvocation{}, rootUnexpected(argument)
		}
		if found, ok := findCommand(argument); ok {
			commandIndex, spec = index, found
			break
		}
		return parsedInvocation{}, unknownCommand(argument)
	}
	if commandIndex < 0 {
		return parsedInvocation{}, "error: a command is required\n\nUsage: skout <command> [flags]\n\nFor more information, try '--help'.\n"
	}
	parsed := parsedInvocation{spec: spec, values: map[string]string{}, booleans: map[string]bool{}}
	terminated := false
	for index := 0; index < len(args); index++ {
		if index == commandIndex {
			continue
		}
		argument := args[index]
		if index > commandIndex && argument == "--" && !terminated {
			terminated = true
			continue
		}
		if !terminated {
			if argument == "-h" || argument == "-?" || argument == "--help" {
				parsed.help = true
				continue
			}
			if argument == "-v" || argument == "--version" {
				parsed.version = true
				continue
			}
			if matched, value, handled, needNext := matchFlag(argument, append(globalFlags, spec.flags...)); handled {
				key := strings.TrimPrefix(matched.long, "--")
				if matched.value == "" {
					if parsed.booleans[key] {
						return parsedInvocation{}, duplicate(spec, matched)
					}
					parsed.booleans[key] = true
					if key == "debug" {
						parsed.debug = true
					}
					continue
				}
				if _, exists := parsed.values[key]; exists {
					return parsedInvocation{}, duplicate(spec, matched)
				}
				if needNext {
					if matched.optional && (index+1 >= len(args) || strings.HasPrefix(args[index+1], "-")) {
						parsed.values[key] = "true"
						continue
					}
					if index+1 >= len(args) || index+1 == commandIndex || strings.HasPrefix(args[index+1], "-") {
						return parsedInvocation{}, missingValue(matched)
					}
					index++
					value = args[index]
				}
				parsed.values[key] = value
				if key == "league" {
					parsed.league, parsed.leagueSet = value, true
				}
				continue
			}
			if strings.HasPrefix(argument, "-") {
				return parsedInvocation{}, unexpected(spec, argument)
			}
		}
		parsed.positionals = append(parsed.positionals, argument)
	}
	if parsed.help || parsed.version {
		return parsed, ""
	}
	if len(parsed.positionals) < spec.minimum {
		return parsedInvocation{}, missingPositional(spec, len(parsed.positionals))
	}
	if (spec.name == "m" || spec.name == "mm") && parsed.values["week"] != "" {
		week, err := strconv.Atoi(parsed.values["week"])
		if err != nil {
			return parsedInvocation{}, fmt.Sprintf("error: invalid value '%s' for '--week <WEEK>': invalid digit found in string\n\nFor more information, try '--help'.\n", parsed.values["week"])
		}
		if week <= 0 {
			return parsedInvocation{}, fmt.Sprintf("error: invalid value '%s' for '--week <WEEK>': value must be positive\n\nFor more information, try '--help'.\n", parsed.values["week"])
		}
	}
	if spec.name == "m" && parsed.values["day"] != "" && !validShortDay(parsed.values["day"]) {
		return parsedInvocation{}, fmt.Sprintf("error: invalid value '%s' for '--day <MMM-DD>': expected a real date in MMM-DD form\n\nFor more information, try '--help'.\n", parsed.values["day"])
	}
	if parsed.values["season"] != "" {
		season, err := strconv.Atoi(parsed.values["season"])
		if err != nil {
			return parsedInvocation{}, fmt.Sprintf("error: invalid value '%s' for '--season <SEASON>': invalid digit found in string\n\nFor more information, try '--help'.\n", parsed.values["season"])
		}
		if season <= 0 {
			return parsedInvocation{}, fmt.Sprintf("error: invalid value '%s' for '--season <SEASON>': value must be positive\n\nFor more information, try '--help'.\n", parsed.values["season"])
		}
	}
	if spec.maximum >= 0 && len(parsed.positionals) > spec.maximum {
		return parsedInvocation{}, extraPositional(spec, parsed.positionals[spec.maximum])
	}
	if spec.name == "m" {
		left, right, selectedUsage := "", "", ""
		switch {
		case parsed.values["week"] != "" && parsed.booleans["weekly"]:
			left, right, selectedUsage = "--week <WEEK>", "--weekly", "skout m --week <WEEK> [NAME]"
		case parsed.values["week"] != "" && parsed.values["day"] != "":
			left, right, selectedUsage = "--week <WEEK>", "--day <MMM-DD>", "skout m --week <WEEK> [NAME]"
		case parsed.booleans["weekly"] && parsed.values["day"] != "":
			left, right, selectedUsage = "--weekly", "--day <MMM-DD>", "skout m --weekly [NAME]"
		}
		if left != "" {
			return parsedInvocation{}, fmt.Sprintf("error: the argument '%s' cannot be used with '%s'\n\nUsage: %s\n\nFor more information, try '--help'.\n", left, right, selectedUsage)
		}
	}
	return parsed, ""
}

func seasonValue(parsed parsedInvocation) int {
	season, _ := strconv.Atoi(parsed.values["season"])
	return season
}

func firstPositional(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func validShortDay(value string) bool {
	if len(value) != 6 || value[3] != '-' {
		return false
	}
	month := strings.ToUpper(value[:1]) + strings.ToLower(value[1:3])
	_, err := time.Parse("Jan-02-2006", month+value[3:]+"-2000")
	return err == nil
}

func parseRootOnly(args []string) (parsedInvocation, string, bool) {
	parsed := parsedInvocation{values: map[string]string{}, booleans: map[string]bool{}}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			if index+1 == len(args) {
				return parsed, "", true
			}
			return parsedInvocation{}, rootTerminatedUnexpected(args[index+1]), true
		}
		matched, value, handled, needNext := matchFlag(argument, globalFlags)
		if !handled {
			return parsedInvocation{}, "", false
		}
		key := strings.TrimPrefix(matched.long, "--")
		if matched.value == "" {
			if parsed.booleans[key] {
				return parsedInvocation{}, rootDuplicate(matched), true
			}
			parsed.booleans[key] = true
			if key == "debug" {
				parsed.debug = true
			}
			continue
		}
		if _, exists := parsed.values[key]; exists {
			return parsedInvocation{}, rootDuplicate(matched), true
		}
		if needNext {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return parsedInvocation{}, missingValue(matched), true
			}
			index++
			value = args[index]
		}
		parsed.values[key] = value
		if key == "league" {
			parsed.league, parsed.leagueSet = value, true
		}
	}
	return parsed, "", true
}

func matchFlag(argument string, flags []flagSpec) (flagSpec, string, bool, bool) {
	for _, flag := range flags {
		if argument == flag.short || argument == flag.long {
			return flag, "", true, flag.value != ""
		}
		if flag.value != "" {
			if value, ok := strings.CutPrefix(argument, flag.long+"="); ok {
				return flag, value, true, false
			}
			if flag.short == "-l" && strings.HasPrefix(argument, "-l") && len(argument) > 2 {
				return flag, strings.TrimPrefix(strings.TrimPrefix(argument, "-l"), "="), true, false
			}
		}
	}
	return flagSpec{}, "", false, false
}

func findCommand(value string) (commandSpec, bool) {
	for _, command := range commands {
		if command.name == value {
			return command, true
		}
		if slices.Contains(command.aliases, value) {
			return command, true
		}
	}
	return commandSpec{}, false
}

func rootAction(args []string) string {
	for index := 0; index < len(args); index++ {
		switch argument := args[index]; argument {
		case "-l", "--league":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return ""
			}
			index++
		case "-d", "--debug":
		case "-h", "-?", "--help", "help":
			return "help"
		case "-v", "--version", "version":
			return "version"
		default:
			if strings.HasPrefix(argument, "--league=") || strings.HasPrefix(argument, "-l") && len(argument) > 2 {
				continue
			}
			return ""
		}
	}
	return ""
}

// RootHelp renders root help from the same descriptors used by parsing.
func RootHelp(version string, mode terminal.ColorMode) string {
	rows := make([]helpRow, 0, 4*len(commands))
	for _, command := range commands {
		if command.name == "fetch" {
			continue
		}
		rows = append(rows, helpRow{command.label, command.description})
		for _, flag := range command.flags {
			rows = append(rows, helpRow{"  " + flagLabel(flag), flag.description})
		}
	}
	rows = append(rows, helpRow{"help", "Print this help"}, helpRow{"version", "Print version"})
	return renderHelp(version, mode, "skout COMMAND [options]", rows, nil)
}

// CommandHelp renders one command's help page through the shared root renderer.
func CommandHelp(command commandSpec, version string, mode terminal.ColorMode) string {
	name, arguments, _ := strings.Cut(command.label, " ")
	synopsis := "skout " + name + " [options]"
	if arguments != "" {
		synopsis += " " + arguments
	}
	return renderHelp(version, mode, synopsis, []helpRow{{command.label, command.description}}, command.flags)
}

type helpRow struct{ form, meaning string }

// renderHelp writes the shared header followed by the Usage, Commands, and Options sections.
func renderHelp(version string, mode terminal.ColorMode, synopsis string, commandRows []helpRow, flags []flagSpec) string {
	var output strings.Builder
	output.WriteString(terminal.Title("skout", mode) + " v" + version + "\n")
	output.WriteString(terminal.Subtitle("Fantasy Baseball advisor", mode) + "\n")
	output.WriteString(terminal.Link("github.com/queone/skout", mode) + "\n\n")
	helpSection(&output, "Usage", []helpRow{{synopsis, ""}}, mode)
	output.WriteByte('\n')
	helpSection(&output, "Commands", commandRows, mode)
	output.WriteByte('\n')
	options := make([]helpRow, 0, len(flags)+len(globalFlags)+2)
	for _, flag := range flags {
		options = append(options, helpRow{flagLabel(flag), flag.description})
	}
	for _, flag := range globalFlags {
		options = append(options, helpRow{flagLabel(flag), flag.description})
	}
	options = append(options, helpRow{"-v, --version", "Print version"}, helpRow{"-h, -?, --help", "Print this help"})
	helpSection(&output, "Options", options, mode)
	return output.String()
}

// helpSection writes one heading and its rows with every meaning aligned two spaces past the longest form.
func helpSection(output *strings.Builder, heading string, rows []helpRow, mode terminal.ColorMode) {
	output.WriteString(terminal.Section(heading, mode) + "\n")
	width := 0
	for _, row := range rows {
		width = max(width, len(row.form))
	}
	for _, row := range rows {
		if row.meaning == "" {
			output.WriteString("  " + row.form + "\n")
			continue
		}
		fmt.Fprintf(output, "  %-*s  %s\n", width, row.form, row.meaning)
	}
}

func usage(command commandSpec) string {
	var output strings.Builder
	output.WriteString("skout " + command.name + " [OPTIONS]")
	if command.positional != "" {
		if command.minimum > 0 {
			for name := range strings.FieldsSeq(command.positional) {
				output.WriteString(" <" + name + ">")
			}
		} else {
			output.WriteString(" [" + command.positional + "]")
		}
	}
	return output.String()
}
func flagLabel(flag flagSpec) string {
	value := flag.value
	label := flag.short + ", " + flag.long
	if value != "" {
		if flag.optional {
			label += " [<" + value + ">]"
		} else {
			label += " <" + value + ">"
		}
	}
	return label
}

func runGlossary(parsed parsedInvocation, context Context) int {
	entries, err := glossary.EmbeddedEntries()
	if err != nil {
		fmt.Fprintf(context.Stderr, "i: load embedded glossary: %v; reinstall skout\n", err)
		return 1
	}
	mode := colorMode(context)
	if len(parsed.positionals) == 0 {
		fmt.Fprintln(context.Stdout, glossary.RenderFullWithMode(entries, mode))
		return 0
	}
	term := strings.TrimSpace(parsed.positionals[0])
	if term == "" {
		io.WriteString(context.Stderr, "i: empty term; provide a glossary key or omit TERM for the full glossary\n")
		return 1
	}
	result := glossary.Lookup(entries, term)
	if result.Entry != nil {
		fmt.Fprintln(context.Stdout, glossary.RenderEntryWithMode(result.Entry, mode))
		return 0
	}
	if len(result.Matches) == 0 {
		fmt.Fprintf(context.Stderr, "i: no glossary entry matches %s; closest keys: %s\n", strconv.Quote(term), strings.Join(result.Suggestions, ", "))
		return 1
	}
	keys := make([]string, 0, len(result.Matches))
	for _, entry := range result.Matches {
		keys = append(keys, entry.Key)
	}
	if !context.StdinIsTerminal || !context.StderrIsTerminal {
		fmt.Fprintf(context.Stderr, "i: term %s is ambiguous; matches: %s; retry with an exact key\n", strconv.Quote(term), strings.Join(keys, ", "))
		return 1
	}
	fmt.Fprintln(context.Prompt, "Multiple matches:")
	for index, entry := range result.Matches {
		fmt.Fprintf(context.Prompt, "  %d) %s — %s [%s]\n", index+1, entry.Key, entry.Term, entry.Class)
	}
	fmt.Fprintf(context.Prompt, "Select a term [1-%d]: ", len(result.Matches))
	if flusher, ok := context.Prompt.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			fmt.Fprintln(context.Stderr, "i: prompt for glossary selection: write failed")
			return 1
		}
	}
	answer, readErr := bufio.NewReader(context.Stdin).ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		fmt.Fprintln(context.Stderr, "i: read glossary selection: failed")
		return 1
	}
	selected, parseErr := strconv.Atoi(strings.TrimSpace(answer))
	if parseErr != nil || selected < 1 || selected > len(result.Matches) {
		fmt.Fprintf(context.Stderr, "i: invalid selection; enter a number from 1 through %d\n", len(result.Matches))
		return 1
	}
	fmt.Fprintln(context.Stdout)
	fmt.Fprintln(context.Stdout, glossary.RenderEntryWithMode(result.Matches[selected-1], mode))
	return 0
}

func colorMode(context Context) terminal.ColorMode {
	return terminal.SelectColorMode(terminal.ColorContext{StdoutIsTerminal: context.StdoutIsTerminal, NoColor: context.NoColor, Term: context.Term, ColorTerm: context.ColorTerm})
}
func rootUnexpected(value string) string {
	return fmt.Sprintf("error: unexpected argument '%s' found\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n", value)
}
func rootTerminatedUnexpected(value string) string {
	tip := ""
	if _, exists := findCommand(value); exists {
		tip = fmt.Sprintf("\n  tip: subcommand '%s' exists; to use it, remove the '--' before it\n", value)
	}
	return fmt.Sprintf("error: unexpected argument '%s' found\n%s\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n", value, tip)
}
func unknownCommand(value string) string {
	return fmt.Sprintf("error: unrecognized subcommand '%s'\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n", value)
}
func unexpected(command commandSpec, value string) string {
	tip := ""
	if command.positional != "" {
		tip = fmt.Sprintf("\n  tip: to pass '%s' as a value, use '-- %s'\n", value, value)
	}
	return fmt.Sprintf("error: unexpected argument '%s' found\n%s\nUsage: %s\n\nFor more information, try '--help'.\n", value, tip, usage(command))
}
func duplicate(command commandSpec, flag flagSpec) string {
	return fmt.Sprintf("error: the argument '%s' cannot be used multiple times\n\nUsage: %s\n\nFor more information, try '--help'.\n", flag.long+func() string {
		if flag.value != "" {
			return " <" + flag.value + ">"
		}
		return ""
	}(), usage(command))
}
func rootDuplicate(flag flagSpec) string {
	label := flag.long
	if flag.value != "" {
		label += " <" + flag.value + ">"
	}
	return fmt.Sprintf("error: the argument '%s' cannot be used multiple times\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n", label)
}
func missingValue(flag flagSpec) string {
	return fmt.Sprintf("error: a value is required for '%s <%s>' but none was supplied\n\nFor more information, try '--help'.\n", flag.long, flag.value)
}
func missingPositional(command commandSpec, provided int) string {
	names := strings.Fields(command.positional)
	missing := names[provided:command.minimum]
	var rows strings.Builder
	for _, name := range missing {
		rows.WriteString("  <" + name + ">\n")
	}
	commandUsage := usage(command)
	if command.name == "fetch" {
		commandUsage = "skout fetch <HOST> <PATH>"
	}
	return fmt.Sprintf("error: the following required arguments were not provided:\n%s\nUsage: %s\n", rows.String(), commandUsage)
}
func extraPositional(command commandSpec, value string) string {
	if command.positional == "" || command.name == "fetch" {
		footer := "\n\nFor more information, try '--help'.\n"
		if command.name == "fetch" {
			footer = "\n"
		}
		return fmt.Sprintf("error: unexpected argument '%s' found\n\nUsage: %s%s", value, usage(command), footer)
	}
	positional := command.positional
	return fmt.Sprintf("error: unexpected value '%s' for '[%s]' found; no more were expected\n\nUsage: %s\n\nFor more information, try '--help'.\n", value, positional, usage(command))
}
