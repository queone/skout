package main

import (
	"fmt"
	"io"
	"os"
)

const programVersion = "0.1.0"

const rootHelp = `skout v0.1.0
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

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stdout, rootHelp)
		return 0
	}
	if len(args) == 1 {
		switch args[0] {
		case "-h", "-?", "--help":
			_, _ = io.WriteString(stdout, rootHelp)
			return 0
		case "-v", "--version":
			_, _ = fmt.Fprintf(stdout, "skout %s\n", programVersion)
			return 0
		}
	}

	_, _ = io.WriteString(stderr, notImplementedMessage)
	return 2
}
