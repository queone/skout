package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/queone/skout/internal/terminal"
)

func TestCompleteCommandGrammarProvidesAllHelpForms(t *testing.T) {
	for _, command := range commands {
		for _, flag := range []string{"-h", "-?", "--help"} {
			var stdout, stderr bytes.Buffer
			code := Run([]string{command.name, flag}, "0.2.0", Context{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Prompt: &stderr}, Handlers{})
			if code != 0 || stdout.Len() == 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), command.description) || !strings.Contains(stdout.String(), "Usage:") {
				t.Errorf("%s %s code=%d stdout=%q stderr=%q", command.name, flag, code, stdout.String(), stderr.String())
			}
		}
	}
}

func TestFantasyContractDispatchesEveryValueOnce(t *testing.T) {
	type contractCase struct {
		Name   string   `json:"name"`
		Args   []string `json:"args"`
		Code   int      `json:"code"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
	}
	data, err := os.ReadFile("testdata/fantasy-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []contractCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			calls := 0
			handlers := Handlers{
				Matchup: func(league, team string, week int, weekly bool, day string, debug bool) (string, error) {
					calls++
					return strings.Join([]string{"m", league, team, strconv.Itoa(week), boolText(weekly), day, boolText(debug)}, ":") + "\n", nil
				},
				Roster: func(league, team string, debug bool) (string, error) {
					calls++
					return strings.Join([]string{"r", league, team, boolText(debug)}, ":") + "\n", nil
				},
				RosterTotals: func(league, weekly string, debug bool) (string, error) {
					calls++
					return strings.Join([]string{"rt", league, weekly, boolText(debug)}, ":") + "\n", nil
				},
				Hitters: func(league, argument, sort, position string, waiver, debug bool) (string, error) {
					calls++
					return strings.Join([]string{"h", league, argument, sort, position, boolText(waiver), boolText(debug)}, ":") + "\n", nil
				},
				Pitchers: func(league, argument, sort, position string, waiver, debug bool) (string, error) {
					calls++
					if argument == "fail" {
						return "", errors.New("injected pitcher failure")
					}
					return strings.Join([]string{"p", league, argument, sort, position, boolText(waiver), boolText(debug)}, ":") + "\n", nil
				},
			}
			var stdout, stderr bytes.Buffer
			code := Run(test.Args, "0.4.0", Context{Stdout: &stdout, Stderr: &stderr}, handlers)
			if code != test.Code || stdout.String() != test.Stdout || stderr.String() != test.Stderr || calls != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q calls=%d", code, stdout.String(), stderr.String(), calls)
			}
		})
	}
}

func TestMatchupSyntaxRejectsBeforeHandler(t *testing.T) {
	for _, args := range [][]string{{"m", "-w", "0"}, {"m", "-D", "Feb-30"}, {"m", "-W", "-D", "Apr-01"}} {
		called := false
		var stdout, stderr bytes.Buffer
		code := Run(args, "0.4.0", Context{Stdout: &stdout, Stderr: &stderr}, Handlers{Matchup: func(string, string, int, bool, string, bool) (string, error) { called = true; return "", nil }})
		if code != 2 || called || stdout.Len() != 0 || !strings.Contains(stderr.String(), "error:") {
			t.Fatalf("args=%v code=%d called=%v stdout=%q stderr=%q", args, code, called, stdout.String(), stderr.String())
		}
	}
}

func TestGovernedAndFrozenCommandHelpLayoutsAreExact(t *testing.T) {
	glossaryHelp, err := os.ReadFile("../../cmd/skout/testdata/glossary-help.txt")
	if err != nil {
		t.Fatal(err)
	}
	command, _ := findCommand("i")
	if got := CommandHelp(command, terminal.Plain); got != string(glossaryHelp) {
		t.Fatalf("glossary help differs\nGOT:\n%s\nWANT:\n%s", got, glossaryHelp)
	}
	command, _ = findCommand("t")
	want := "Show MLB 40-man rosters\n\nUsage: skout t [OPTIONS] [TEAM]\n\nArguments:\n  [TEAM]  \n\nOptions:\n  -f, --force         Refresh provider data\n  -h, -?, --help      \n  -l, --league <KEY>  Yahoo league key\n  -d, --debug         Print operation diagnostics\n"
	if got := CommandHelp(command, terminal.Plain); got != want {
		t.Fatalf("team help differs\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestFrozenParserDiagnosticsRemainExact(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"i", "pa", "extra"}, "error: unexpected value 'extra' for '[TERM]' found; no more were expected\n\nUsage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n"},
		{[]string{"i", "--wat"}, "error: unexpected argument '--wat' found\n\n  tip: to pass '--wat' as a value, use '-- --wat'\n\nUsage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n"},
		{[]string{"i", "--league"}, "error: a value is required for '--league <KEY>' but none was supplied\n\nFor more information, try '--help'.\n"},
		{[]string{"m", "--week", "nope"}, "error: invalid value 'nope' for '--week <WEEK>': invalid digit found in string\n\nFor more information, try '--help'.\n"},
		{[]string{"unknown"}, "error: unrecognized subcommand 'unknown'\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(test.args, "0.2.0", Context{Stdout: &stdout, Stderr: &stderr}, Handlers{}); code != 2 || stdout.Len() != 0 || stderr.String() != test.want {
			t.Errorf("args=%v code=%d stdout=%q\nGOT %q\nWANT %q", test.args, code, stdout.String(), stderr.String(), test.want)
		}
	}
}

func TestCompoundRootHelpAndVersionUseFrozenParserActions(t *testing.T) {
	for _, test := range []struct {
		args     []string
		contains string
	}{{[]string{"--help", "extra"}, "Usage: skout [OPTIONS] [COMMAND]"}, {[]string{"-d", "--help"}, "Commands:\n  fetch"}, {[]string{"--version", "extra"}, "skout 0.2.0\n"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(test.args, "0.2.0", Context{Stdout: &stdout, Stderr: &stderr}, Handlers{}); code != 0 || !strings.Contains(stdout.String(), test.contains) || stderr.Len() != 0 {
			t.Errorf("args=%v code=%d stdout=%q stderr=%q", test.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestRootHelpAndGlossaryPlainBehaviorRemainFrozen(t *testing.T) {
	want, err := os.ReadFile("../../cmd/skout/testdata/root-help.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"-h"}, {"-?"}, {"--help"}} {
		var stdout, stderr bytes.Buffer
		code := Run(args, "0.5.0", Context{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, Handlers{})
		if code != 0 || !bytes.Equal(stdout.Bytes(), want) || stderr.Len() != 0 {
			t.Errorf("root %v code=%d stdout differs=%v stderr=%q", args, code, !bytes.Equal(stdout.Bytes(), want), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"whatis", "pa"}, "0.2.0", Context{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, Handlers{})
	digest := sha256.Sum256(stdout.Bytes())
	if code != 0 || hex.EncodeToString(digest[:]) != "1646f1122727690c7411af94fe2c1c387848de0ef4f4c9764a4f270f08699759" || stderr.Len() != 0 {
		t.Fatalf("glossary code=%d digest=%s stderr=%q", code, hex.EncodeToString(digest[:]), stderr.String())
	}
}

func TestControlledNonFantasyContractDispatchesStreamsAndExitCodes(t *testing.T) {
	type contractCase struct {
		Name   string   `json:"name"`
		Args   []string `json:"args"`
		Code   int      `json:"code"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
	}
	data, err := os.ReadFile("testdata/non-fantasy-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []contractCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	debugReachedHandler := false
	handlers := Handlers{
		Status: func(league string) (string, error) { return "status:" + league + "\n", nil },
		Sync: func(league, team string, debug bool, output io.Writer) (string, error) {
			_, _ = io.WriteString(output, "sync-progress\n")
			return "sync:" + league + ":" + team + ":" + boolText(debug) + "\n", nil
		},
		Teams: func(team string, force, _ bool) (string, error) {
			return "teams:" + team + ":" + boolText(force) + "\n", nil
		},
		Totals: func(force, debug bool) (string, error) {
			debugReachedHandler = debug
			return "totals:" + boolText(force) + "\n", nil
		},
		Probables: func(force, _ bool) (string, error) { return "probables:" + boolText(force) + "\n", nil },
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(test.Args, "0.2.0", Context{Stdin: strings.NewReader("unused"), Stdout: &stdout, Stderr: &stderr, Prompt: &stderr}, handlers)
			if code != test.Code || stdout.String() != test.Stdout || stderr.String() != test.Stderr {
				t.Errorf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if !debugReachedHandler {
		t.Fatal("debug state did not reach the application handler")
	}
}

func TestSyncDispatchesFrozenFlagPlacementsAndStreamsProgress(t *testing.T) {
	for _, args := range [][]string{
		{"-l", "mlb.l.1", "sync", "-T", "Operators"},
		{"sync", "--team=Operators", "--league=mlb.l.1"},
		{"sync", "-T", "Operators", "-lmlb.l.1"},
	} {
		var stdout, stderr bytes.Buffer
		called := false
		handlers := Handlers{Sync: func(league, team string, debug bool, output io.Writer) (string, error) {
			called = true
			if league != "mlb.l.1" || team != "Operators" || debug {
				t.Fatalf("args=%v league=%q team=%q debug=%v", args, league, team, debug)
			}
			_, _ = io.WriteString(output, "==> Sync started.\n")
			return "==> Sync success.\n", nil
		}}
		code := Run(args, "0.4.0", Context{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Prompt: &stderr}, handlers)
		if code != 0 || !called || stdout.String() != "==> Sync started.\n==> Sync success.\n" || stderr.Len() != 0 {
			t.Errorf("args=%v code=%d called=%v stdout=%q stderr=%q", args, code, called, stdout.String(), stderr.String())
		}
	}
}

func TestGlobalFlagPlacementConflictsDiagnosticsAndDeferredIsolation(t *testing.T) {
	statusCalls := 0
	handlers := Handlers{Status: func(league string) (string, error) { statusCalls++; return league, nil }}
	for _, args := range [][]string{{"-l", "one", "st"}, {"st", "--league=one"}, {"st", "-lone"}, {"-d", "st", "-l", "one"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, "0.2.0", Context{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, handlers); code != 0 || stdout.String() != "one" {
			t.Errorf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	for _, args := range [][]string{{"t", "one", "two"}, {"fetch", "mlb"}, {"tt", "--wat"}, {"m", "--week", "1", "--weekly"}, {"st", "-d", "--debug"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, "0.2.0", Context{Stdout: &stdout, Stderr: &stderr}, handlers); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "error:") {
			t.Errorf("invalid %v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	input := strings.NewReader("yes\n")
	if code := Run([]string{"reset"}, "0.2.0", Context{Stdin: input, Stdout: &stdout, Stderr: &stderr, Prompt: &stderr}, handlers); code != 2 || stderr.String() != DeferredMessage {
		t.Fatalf("reset code=%d stderr=%q", code, stderr.String())
	}
	remaining, _ := io.ReadAll(input)
	if string(remaining) != "yes\n" {
		t.Fatal("reset read confirmation")
	}
	if statusCalls != 4 {
		t.Fatalf("status calls=%d", statusCalls)
	}
}

func TestRootOnlyFlagsAttachedLeagueValuesAndArityDiagnosticsMatchContract(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{args: []string{"-d"}, code: 0, stderr: "skout debug: command=root league_source=saved\n"},
		{args: []string{"-l", "value"}, code: 0},
		{args: []string{"-d", "-l", "value"}, code: 0, stderr: "skout debug: command=root league_source=override\n"},
		{args: []string{"--"}, code: 0},
		{args: []string{"-d", "--"}, code: 0, stderr: "skout debug: command=root league_source=saved\n"},
		{args: []string{"--", "st"}, code: 2, stderr: "error: unexpected argument 'st' found\n\n  tip: subcommand 'st' exists; to use it, remove the '--' before it\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n"},
		{args: []string{"-l", "st"}, code: 0},
		{args: []string{"-l"}, code: 2, stderr: "error: a value is required for '--league <KEY>' but none was supplied\n\nFor more information, try '--help'.\n"},
		{args: []string{"-l", "--help"}, code: 2, stderr: "error: a value is required for '--league <KEY>' but none was supplied\n\nFor more information, try '--help'.\n"},
		{args: []string{"-d", "--debug"}, code: 2, stderr: "error: the argument '--debug' cannot be used multiple times\n\nUsage: skout [OPTIONS] [COMMAND]\n\nFor more information, try '--help'.\n"},
		{args: []string{"st", "-l=one"}, code: 0, stdout: "one\n"},
		{args: []string{"st", "--league="}, code: 0, stdout: "\n"},
		{args: []string{"st", "-l="}, code: 0, stdout: "\n"},
		{args: []string{"fetch"}, code: 2, stderr: "error: the following required arguments were not provided:\n  <HOST>\n  <PATH>\n\nUsage: skout fetch <HOST> <PATH>\n"},
		{args: []string{"fetch", "mlb"}, code: 2, stderr: "error: the following required arguments were not provided:\n  <PATH>\n\nUsage: skout fetch <HOST> <PATH>\n"},
		{args: []string{"fetch", "mlb", "/x", "extra"}, code: 2, stderr: "error: unexpected argument 'extra' found\n\nUsage: skout fetch [OPTIONS] <HOST> <PATH>\n"},
		{args: []string{"reset", "extra"}, code: 2, stderr: "error: unexpected argument 'extra' found\n\nUsage: skout reset [OPTIONS]\n\nFor more information, try '--help'.\n"},
	}
	handlers := Handlers{Status: func(league string) (string, error) { return league + "\n", nil }}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		code := Run(test.args, "0.2.0", Context{Stdout: &stdout, Stderr: &stderr}, handlers)
		if code != test.code || stdout.String() != test.stdout || stderr.String() != test.stderr {
			t.Errorf("args=%v code=%d stdout=%q stderr=%q", test.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestColorAndInteractiveGlossaryUseInjectedEvidenceAndFlushPrompt(t *testing.T) {
	prompt := &flushBuffer{}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"i", "run"}, "0.2.0", Context{Stdin: strings.NewReader("1\n"), Stdout: &stdout, Stderr: &stderr, Prompt: prompt, StdinIsTerminal: true, StdoutIsTerminal: true, StderrIsTerminal: true, Term: "xterm-256color"}, Handlers{})
	if code != 0 || prompt.flushes != 1 || !strings.Contains(prompt.String(), "Select a term") || !strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("code=%d flushes=%d prompt=%q stdout=%q stderr=%q", code, prompt.flushes, prompt.String(), stdout.String(), stderr.String())
	}
	if mode := colorMode(Context{StdoutIsTerminal: true, Term: "dumb", ColorTerm: "truecolor"}); mode != terminal.Plain {
		t.Fatalf("dumb mode=%v", mode)
	}
}

type flushBuffer struct {
	bytes.Buffer
	flushes int
}

func (buffer *flushBuffer) Flush() error { buffer.flushes++; return nil }
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
