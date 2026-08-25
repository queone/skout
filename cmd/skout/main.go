package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/queone/skout/internal/glossary"
	"github.com/queone/skout/internal/terminal"
)

const programVersion = "0.2.0"

const rootHelp = `skout v0.2.0
Fantasy Baseball advisor — github.com/queone/skout

USAGE
  skout <command> [flags]

COMMANDS
  st                           Show status
  sync                         Synchronize the selected league
    -T, --team <TEAM>          Select the primary fantasy team
  reset                        Delete the local skout database
  m [team]                     Show a daily or weekly matchup
    -w, --week <WEEK>          Show a specific matchup week
    -W, --weekly               Show weekly running totals
    -D, --day <MMM-DD>         Show stats for a specific day
  t [team]                     Show MLB 40-man rosters
    -f, --force                Refresh provider data
  tt                           Show MLB standings and team totals
    -f, --force                Refresh provider data
  sp                           Show the three-day probable-pitcher slate
    -f, --force                Refresh provider data
  r [name]                     Show a fantasy roster
  rt                           Show fantasy roster totals
    -w, --weekly [<WEEK|DATE>] Show current or selected weekly totals
  h [N|name]                   Browse hitters or show a player
    -s, --sort <FIELD>         Sort by a displayed field
    -p, --position <POS>       Filter by eligible position
    -w, --waiver               Show available Yahoo pickup players
  p [N|name]                   Browse pitchers or show a player
    -s, --sort <FIELD>         Sort by a displayed field
    -p, --position <POS>       Filter by eligible position
    -w, --waiver               Show available Yahoo pickup players
  i [term]                     Look up a term in the skout glossary

FLAGS
  -l, --league <key>           Yahoo league key
  -d, --debug                  Print operation diagnostics
  -v, --version                Print version
  -h, -?, --help               Print this help
`

const notImplementedMessage = "skout: command not implemented in this migration slice\n"

const (
	glossaryUsage = "Usage: skout i [OPTIONS] [TERM]"
)

type glossaryFlagKind uint8

const (
	glossaryLeagueFlag glossaryFlagKind = iota
	glossaryDebugFlag
	glossaryHelpFlag
)

type glossaryFlagDescriptor struct {
	kind        glossaryFlagKind
	short       string
	aliases     []string
	long        string
	valueName   string
	description string
}

var glossaryFlags = []glossaryFlagDescriptor{
	{kind: glossaryLeagueFlag, short: "-l", long: "--league", valueName: "KEY", description: "Yahoo league key"},
	{kind: glossaryDebugFlag, short: "-d", long: "--debug", description: "Print operation diagnostics"},
	{kind: glossaryHelpFlag, short: "-h", aliases: []string{"-?"}, long: "--help", description: "Print this help"},
}

type flushWriter interface {
	io.Writer
	Flush() error
}

type directFlushWriter struct {
	io.Writer
}

func (directFlushWriter) Flush() error {
	return nil
}

type commandContext struct {
	stdin            io.Reader
	stdout           io.Writer
	stderr           io.Writer
	prompt           flushWriter
	stdinIsTerminal  bool
	stdoutIsTerminal bool
	stderrIsTerminal bool
	noColor          string
	term             string
	colorTerm        string
}

type glossaryArguments struct {
	term      *string
	leagueSet bool
	debug     bool
	help      bool
}

func main() {
	os.Exit(runWithContext(os.Args[1:], commandContext{
		stdin:            os.Stdin,
		stdout:           os.Stdout,
		stderr:           os.Stderr,
		prompt:           directFlushWriter{Writer: os.Stderr},
		stdinIsTerminal:  terminal.IsTerminal(os.Stdin),
		stdoutIsTerminal: terminal.IsTerminal(os.Stdout),
		stderrIsTerminal: terminal.IsTerminal(os.Stderr),
		noColor:          os.Getenv("NO_COLOR"),
		term:             os.Getenv("TERM"),
		colorTerm:        os.Getenv("COLORTERM"),
	}))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithContext(args, commandContext{
		stdin:  strings.NewReader(""),
		stdout: stdout,
		stderr: stderr,
		prompt: directFlushWriter{Writer: stderr},
	})
}

func runWithContext(args []string, context commandContext) int {
	if len(args) == 0 {
		_, _ = io.WriteString(context.stdout, rootHelp)
		return 0
	}
	if len(args) == 1 {
		switch args[0] {
		case "-h", "-?", "--help":
			_, _ = io.WriteString(context.stdout, rootHelp)
			return 0
		case "-v", "--version":
			_, _ = fmt.Fprintf(context.stdout, "skout %s\n", programVersion)
			return 0
		}
	}

	commandIndex := findGlossaryCommand(args)
	if commandIndex < 0 {
		_, _ = io.WriteString(context.stderr, notImplementedMessage)
		return 2
	}
	return runGlossaryInvocation(args, commandIndex, context)
}

func findGlossaryCommand(args []string) int {
	for index := 0; index < len(args); {
		argument := args[index]
		if argument == "i" || argument == "whatis" {
			return index
		}
		descriptor, attachedValue, recognized := matchGlossaryFlag(argument)
		if !recognized || descriptor.kind == glossaryHelpFlag {
			return -1
		}
		if descriptor.kind == glossaryLeagueFlag && attachedValue == "" {
			if argument != descriptor.short && argument != descriptor.long {
				index++
				continue
			}
			if index+1 >= len(args) {
				return -1
			}
			index += 2
			continue
		}
		index++
	}
	return -1
}

func runGlossaryInvocation(args []string, commandIndex int, context commandContext) int {
	parsed, diagnostic := parseGlossaryArguments(args, commandIndex)
	if diagnostic != "" {
		_, _ = io.WriteString(context.stderr, diagnostic)
		return 2
	}

	mode := terminal.SelectColorMode(terminal.ColorContext{
		StdoutIsTerminal: context.stdoutIsTerminal,
		NoColor:          context.noColor,
		Term:             context.term,
		ColorTerm:        context.colorTerm,
	})
	if parsed.help {
		_, _ = io.WriteString(context.stdout, renderGlossaryHelp(mode))
		return 0
	}
	if parsed.debug {
		leagueSource := "saved"
		if parsed.leagueSet {
			leagueSource = "override"
		}
		_, _ = fmt.Fprintf(context.stderr, "skout debug: command=i league_source=%s\n", leagueSource)
	}

	entries, err := glossary.EmbeddedEntries()
	if err != nil {
		_, _ = fmt.Fprintf(context.stderr, "i: load embedded glossary: %v; reinstall skout\n", err)
		return 1
	}
	if parsed.term == nil {
		_, _ = fmt.Fprintln(context.stdout, glossary.RenderFullWithMode(entries, mode))
		return 0
	}
	term := strings.TrimSpace(*parsed.term)
	if term == "" {
		_, _ = io.WriteString(context.stderr, "i: empty term; provide a glossary key or omit TERM for the full glossary\n")
		return 1
	}

	result := glossary.Lookup(entries, term)
	if result.Entry != nil {
		_, _ = fmt.Fprintln(context.stdout, glossary.RenderEntryWithMode(result.Entry, mode))
		return 0
	}
	if len(result.Matches) == 0 {
		_, _ = fmt.Fprintf(
			context.stderr,
			"i: no glossary entry matches %s; closest keys: %s\n",
			strconv.Quote(term),
			strings.Join(result.Suggestions, ", "),
		)
		return 1
	}
	if !context.stdinIsTerminal || !context.stderrIsTerminal {
		_, _ = fmt.Fprintf(
			context.stderr,
			"i: term %s is ambiguous; matches: %s; retry with an exact key\n",
			strconv.Quote(term),
			strings.Join(glossaryMatchKeys(result.Matches), ", "),
		)
		return 1
	}
	return promptForGlossaryMatch(result.Matches, mode, context)
}

func parseGlossaryArguments(args []string, commandIndex int) (glossaryArguments, string) {
	var parsed glossaryArguments
	terminated := false
	for index := 0; index < len(args); index++ {
		if index == commandIndex {
			continue
		}
		argument := args[index]
		if index > commandIndex && !terminated && argument == "--" {
			terminated = true
			continue
		}
		if !terminated {
			descriptor, attachedValue, recognized := matchGlossaryFlag(argument)
			if recognized {
				switch descriptor.kind {
				case glossaryLeagueFlag:
					if parsed.leagueSet {
						return glossaryArguments{}, duplicateGlossaryFlagDiagnostic(descriptor)
					}
					value := attachedValue
					if argument == descriptor.short || argument == descriptor.long {
						if index+1 >= len(args) || index+1 == commandIndex || (strings.HasPrefix(args[index+1], "-") && args[index+1] != "-") {
							return glossaryArguments{}, missingGlossaryFlagValueDiagnostic(descriptor)
						}
						index++
						value = args[index]
					}
					if value == "" {
						return glossaryArguments{}, missingGlossaryFlagValueDiagnostic(descriptor)
					}
					parsed.leagueSet = true
				case glossaryDebugFlag:
					if parsed.debug {
						return glossaryArguments{}, duplicateGlossaryFlagDiagnostic(descriptor)
					}
					parsed.debug = true
				case glossaryHelpFlag:
					parsed.help = true
				}
				continue
			}
			if strings.HasPrefix(argument, "-") && argument != "-" {
				return glossaryArguments{}, unknownGlossaryFlagDiagnostic(argument)
			}
		}
		if parsed.term != nil {
			return glossaryArguments{}, extraGlossaryValueDiagnostic(argument)
		}
		term := argument
		parsed.term = &term
	}
	return parsed, ""
}

func matchGlossaryFlag(argument string) (glossaryFlagDescriptor, string, bool) {
	for _, descriptor := range glossaryFlags {
		if argument == descriptor.short || argument == descriptor.long {
			return descriptor, "", true
		}
		if slices.Contains(descriptor.aliases, argument) {
			return descriptor, "", true
		}
		if descriptor.kind != glossaryLeagueFlag {
			continue
		}
		if after, ok := strings.CutPrefix(argument, descriptor.long+"="); ok {
			return descriptor, after, true
		}
		if strings.HasPrefix(argument, descriptor.short) && len(argument) > len(descriptor.short) {
			return descriptor, strings.TrimPrefix(argument, descriptor.short), true
		}
	}
	return glossaryFlagDescriptor{}, "", false
}

func renderGlossaryHelp(mode terminal.ColorMode) string {
	var output strings.Builder
	output.WriteString("Look up a term in the skout glossary\n\n")
	output.WriteString(terminal.Usage("Usage:", mode))
	output.WriteString(" skout i [OPTIONS] [TERM]\n\nArguments:\n  [TERM]\n\nOptions:\n")
	for _, descriptor := range glossaryFlags {
		writeGlossaryHelpRow(&output, glossaryFlagLabel(descriptor), descriptor.description)
	}
	return output.String()
}

func glossaryFlagLabel(descriptor glossaryFlagDescriptor) string {
	labels := []string{descriptor.short}
	labels = append(labels, descriptor.aliases...)
	labels = append(labels, descriptor.long)
	label := strings.Join(labels, ", ")
	if descriptor.valueName != "" {
		label += " <" + descriptor.valueName + ">"
	}
	return label
}

func writeGlossaryHelpRow(output *strings.Builder, label, description string) {
	const descriptionColumn = 38
	padding := max(descriptionColumn-1-2-len(label), 2)
	output.WriteString("  ")
	output.WriteString(label)
	output.WriteString(strings.Repeat(" ", padding))
	output.WriteString(description)
	output.WriteByte('\n')
}

func duplicateGlossaryFlagDiagnostic(descriptor glossaryFlagDescriptor) string {
	return fmt.Sprintf(
		"error: the argument '%s' cannot be used multiple times\n\n%s\n\nFor more information, try '--help'.\n",
		canonicalGlossaryFlag(descriptor),
		glossaryUsage,
	)
}

func missingGlossaryFlagValueDiagnostic(descriptor glossaryFlagDescriptor) string {
	return fmt.Sprintf(
		"error: a value is required for '%s' but none was supplied\n\nFor more information, try '--help'.\n",
		canonicalGlossaryFlag(descriptor),
	)
}

func canonicalGlossaryFlag(descriptor glossaryFlagDescriptor) string {
	if descriptor.valueName == "" {
		return descriptor.long
	}
	return descriptor.long + " <" + descriptor.valueName + ">"
}

func unknownGlossaryFlagDiagnostic(argument string) string {
	return fmt.Sprintf(
		"error: unexpected argument '%s' found\n\n  tip: to pass '%s' as a value, use '-- %s'\n\n%s\n\nFor more information, try '--help'.\n",
		argument,
		argument,
		argument,
		glossaryUsage,
	)
}

func extraGlossaryValueDiagnostic(argument string) string {
	return fmt.Sprintf(
		"error: unexpected value '%s' for '[TERM]' found; no more were expected\n\n%s\n\nFor more information, try '--help'.\n",
		argument,
		glossaryUsage,
	)
}

func glossaryMatchKeys(entries []*glossary.Entry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

func promptForGlossaryMatch(entries []*glossary.Entry, mode terminal.ColorMode, context commandContext) int {
	if _, err := fmt.Fprintln(context.prompt, "Multiple matches:"); err != nil {
		_, _ = io.WriteString(context.stderr, "i: show glossary choices: write failed\n")
		return 1
	}
	for index, entry := range entries {
		if _, err := fmt.Fprintf(context.prompt, "  %d) %s — %s [%s]\n", index+1, entry.Key, entry.Term, entry.Class); err != nil {
			_, _ = io.WriteString(context.stderr, "i: show glossary choices: write failed\n")
			return 1
		}
	}
	if _, err := fmt.Fprintf(context.prompt, "Select a term [1-%d]: ", len(entries)); err != nil {
		_, _ = io.WriteString(context.stderr, "i: prompt for glossary selection: write failed\n")
		return 1
	}
	if err := context.prompt.Flush(); err != nil {
		_, _ = io.WriteString(context.stderr, "i: prompt for glossary selection: write failed\n")
		return 1
	}

	answer, err := bufio.NewReader(context.stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		_, _ = io.WriteString(context.stderr, "i: read glossary selection: failed\n")
		return 1
	}
	selected := glossary.SelectMatch(entries, answer)
	if selected == nil {
		_, _ = fmt.Fprintf(context.stderr, "i: invalid selection; enter a number from 1 through %d\n", len(entries))
		return 1
	}
	_, _ = fmt.Fprintf(context.stdout, "\n%s\n", glossary.RenderEntryWithMode(selected, mode))
	return 0
}
