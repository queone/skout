package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

const rootHelpSHA256 = "bdfcae59ea42d480f2252935d25083596dad444a126f8a61e71965fb08b9042b"

func TestRootHelpFormsMatchFrozenBaseline(t *testing.T) {
	golden := rootHelpGolden(t)
	digest := sha256.Sum256(golden)
	if got := hex.EncodeToString(digest[:]); got != rootHelpSHA256 {
		t.Fatalf("root help fixture SHA-256 = %s, want %s", got, rootHelpSHA256)
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "NoArguments"},
		{name: "ShortHelp", args: []string{"-h"}},
		{name: "QuestionHelp", args: []string{"-?"}},
		{name: "LongHelp", args: []string{"--help"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := execute(test.args)
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if !bytes.Equal(stdout, golden) {
				t.Errorf("stdout does not match testdata/root-help.txt\n%s", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestVersionFormsMatchGoRelease(t *testing.T) {
	for _, form := range []string{"-v", "--version"} {
		t.Run(form, func(t *testing.T) {
			code, stdout, stderr := execute([]string{form})
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if got, want := string(stdout), "skout 0.2.0\n"; got != want {
				t.Errorf("stdout = %q, want %q", got, want)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestUnportedInvocationsFailClosed(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	emptyDirectory := t.TempDir()
	if err := os.Chdir(emptyDirectory); err != nil {
		t.Fatalf("enter empty working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	tests := []struct {
		name string
		args []string
	}{
		{name: "Fetch", args: []string{"fetch"}},
		{name: "Status", args: []string{"st"}},
		{name: "Sync", args: []string{"sync"}},
		{name: "Reset", args: []string{"reset"}},
		{name: "Matchup", args: []string{"m"}},
		{name: "Teams", args: []string{"t"}},
		{name: "TeamTotals", args: []string{"tt"}},
		{name: "ProbablePitchers", args: []string{"sp"}},
		{name: "Roster", args: []string{"r"}},
		{name: "RosterTotals", args: []string{"rt"}},
		{name: "Hitters", args: []string{"h"}},
		{name: "Pitchers", args: []string{"p"}},
		{name: "Unknown", args: []string{"unknown"}},
		{name: "LeagueFlag", args: []string{"-l"}},
		{name: "DebugFlag", args: []string{"-d"}},
		{name: "HelpWithExtraArgument", args: []string{"--help", "extra"}},
		{name: "VersionWithExtraArgument", args: []string{"--version", "extra"}},
		{name: "MultipleArguments", args: []string{"unknown", "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := execute(test.args)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if got := string(stderr); got != notImplementedMessage {
				t.Errorf("stderr = %q, want %q", got, notImplementedMessage)
			}
		})
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read working directory after invocations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("unported invocations created %d filesystem entries", len(entries))
	}
}

func TestGlossaryHelpFormsMatchGovernedFixture(t *testing.T) {
	golden, err := os.ReadFile("testdata/glossary-help.txt")
	if err != nil {
		t.Fatalf("read glossary help fixture: %v", err)
	}
	for _, command := range []string{"i", "whatis"} {
		for _, flag := range []string{"-h", "-?", "--help"} {
			name := command + flag
			t.Run(name, func(t *testing.T) {
				code, stdout, stderr := execute([]string{command, flag})
				if code != 0 {
					t.Errorf("exit code = %d, want 0", code)
				}
				if !bytes.Equal(stdout, golden) {
					t.Errorf("stdout does not match testdata/glossary-help.txt\n%s", stdout)
				}
				if len(stderr) != 0 {
					t.Errorf("stderr = %q, want empty", stderr)
				}
			})
		}
	}

	plain := string(golden)
	for _, row := range []struct {
		label       string
		description string
	}{
		{label: "-l, --league <KEY>", description: "Yahoo league key"},
		{label: "-d, --debug", description: "Print operation diagnostics"},
		{label: "-h, -?, --help", description: "Print this help"},
	} {
		lineStart := strings.Index(plain, "  "+row.label)
		if lineStart < 0 {
			t.Errorf("help is missing flag row %q", row.label)
			continue
		}
		lineEnd := strings.IndexByte(plain[lineStart:], '\n')
		line := plain[lineStart : lineStart+lineEnd]
		if got, want := strings.Index(line, row.description), 37; got != want {
			t.Errorf("%s description index = %d, want %d", row.label, got, want)
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runWithContext([]string{"i", "--help"}, commandContext{
		stdin:            strings.NewReader(""),
		stdout:           &stdout,
		stderr:           &stderr,
		stdoutIsTerminal: true,
		term:             "xterm-256color",
	})
	wantColored := strings.Replace(plain, "Usage:", "\x1b[1;38;5;231mUsage:\x1b[0m", 1)
	if code != 0 || stdout.String() != wantColored || stderr.Len() != 0 {
		t.Errorf("colored help = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestGlossaryLookupAndAliasMatchFrozenOutput(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	emptyDirectory := t.TempDir()
	if err := os.Chdir(emptyDirectory); err != nil {
		t.Fatalf("enter empty working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	t.Setenv("HOME", "")

	var canonical []byte
	for _, command := range []string{"i", "whatis"} {
		code, stdout, stderr := execute([]string{command, "pa"})
		if code != 0 {
			t.Errorf("%s exit code = %d, want 0", command, code)
		}
		if len(stderr) != 0 {
			t.Errorf("%s stderr = %q, want empty", command, stderr)
		}
		digest := sha256.Sum256(stdout)
		if got, want := hex.EncodeToString(digest[:]), "1646f1122727690c7411af94fe2c1c387848de0ef4f4c9764a4f270f08699759"; got != want {
			t.Errorf("%s stdout SHA-256 = %s, want %s", command, got, want)
		}
		if canonical == nil {
			canonical = stdout
		} else if !bytes.Equal(stdout, canonical) {
			t.Errorf("%s output differs from i", command)
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read working directory after glossary invocations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("glossary invocations created %d filesystem entries", len(entries))
	}
}

func TestFullGlossaryMatchesFrozenDigestInPlainMode(t *testing.T) {
	code, stdout, stderr := execute([]string{"i"})
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if bytes.Contains(stdout, []byte("\x1b[")) {
		t.Error("plain glossary contains ANSI")
	}
	digest := sha256.Sum256(stdout)
	if got, want := hex.EncodeToString(digest[:]), "d1ca06f25a22f4bf40d1d0c23e3f04d9b5404502dc620e109bc26799af9b4f08"; got != want {
		t.Errorf("full stdout SHA-256 = %s, want %s", got, want)
	}
}

func TestGlossaryInputErrorsMatchFrozenDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "WhitespaceOnly",
			args:       []string{"i", "   "},
			wantCode:   1,
			wantStderr: "i: empty term; provide a glossary key or omit TERM for the full glossary\n",
		},
		{
			name:       "Miss",
			args:       []string{"i", "definitely-not-a-key"},
			wantCode:   1,
			wantStderr: "i: no glossary entry matches \"definitely-not-a-key\"; closest keys: empirical_bayes, exit_velo, lineup_candidates\n",
		},
		{
			name:       "NonInteractiveAmbiguity",
			args:       []string{"i", "run"},
			wantCode:   1,
			wantStderr: "i: term \"run\" is ambiguous; matches: era, hr, r; retry with an exact key\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := execute(test.args)
			if code != test.wantCode || len(stdout) != 0 || string(stderr) != test.wantStderr {
				t.Errorf("result = code %d, stdout %q, stderr %q; want code %d, empty stdout, stderr %q", code, stdout, stderr, test.wantCode, test.wantStderr)
			}
		})
	}
}

func TestGlossaryUsageErrorsMatchFrozenDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name: "ExtraValue",
			args: []string{"i", "pa", "extra"},
			wantStderr: "error: unexpected value 'extra' for '[TERM]' found; no more were expected\n\n" +
				"Usage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n",
		},
		{
			name:       "MissingLeagueValue",
			args:       []string{"i", "--league"},
			wantStderr: "error: a value is required for '--league <KEY>' but none was supplied\n\nFor more information, try '--help'.\n",
		},
		{
			name: "UnknownFlag",
			args: []string{"i", "--wat"},
			wantStderr: "error: unexpected argument '--wat' found\n\n  tip: to pass '--wat' as a value, use '-- --wat'\n\n" +
				"Usage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n",
		},
		{
			name: "RepeatedDebug",
			args: []string{"i", "-d", "--debug", "pa"},
			wantStderr: "error: the argument '--debug' cannot be used multiple times\n\n" +
				"Usage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n",
		},
		{
			name: "RepeatedLeague",
			args: []string{"i", "-l", "one", "--league=two", "pa"},
			wantStderr: "error: the argument '--league <KEY>' cannot be used multiple times\n\n" +
				"Usage: skout i [OPTIONS] [TERM]\n\nFor more information, try '--help'.\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := execute(test.args)
			if code != 2 || len(stdout) != 0 || string(stderr) != test.wantStderr {
				t.Errorf("result = code %d, stdout %q, stderr %q; want code 2, empty stdout, stderr %q", code, stdout, stderr, test.wantStderr)
			}
		})
	}
}

func TestGlossaryArgumentGrammarAcceptsSettledForms(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{name: "AttachedShortLeague", args: []string{"i", "-lmlb.l.1", "pa"}},
		{name: "LongEqualsLeague", args: []string{"i", "--league=mlb.l.1", "pa"}},
		{name: "LeagueBeforeCommand", args: []string{"--league", "mlb.l.1", "i", "pa"}},
		{name: "LeagueAfterTerm", args: []string{"i", "pa", "-l", "mlb.l.1"}},
		{name: "DebugAfterTerm", args: []string{"i", "pa", "--debug"}, wantStderr: "skout debug: command=i league_source=saved\n"},
		{name: "DebugAndLeagueOverride", args: []string{"-d", "whatis", "pa", "--league=mlb.l.1"}, wantStderr: "skout debug: command=i league_source=override\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := execute(test.args)
			if code != 0 || !bytes.HasPrefix(stdout, []byte("Plate Appearance (pa) [baseball]\n")) || string(stderr) != test.wantStderr {
				t.Errorf("result = code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}

	code, stdout, stderr := execute([]string{"i", "--", "-?"})
	wantStderr := "i: no glossary entry matches \"-?\"; closest keys: ab, bb, cs\n"
	if code != 1 || len(stdout) != 0 || string(stderr) != wantStderr {
		t.Errorf("terminator result = code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestInteractiveGlossarySelectionUsesInjectedStreams(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompt := &recordingFlushWriter{Writer: &stderr}
	code := runWithContext([]string{"i", "run"}, commandContext{
		stdin:            strings.NewReader("2\n"),
		stdout:           &stdout,
		stderr:           &stderr,
		prompt:           prompt,
		stdinIsTerminal:  true,
		stderrIsTerminal: true,
	})
	wantPrompt := "Multiple matches:\n  1) era — Earned Run Average [stat]\n  2) hr — Home Run [stat]\n  3) r — Runs [stat]\nSelect a term [1-3]: "
	if code != 0 || stderr.String() != wantPrompt {
		t.Errorf("selection result = code %d, stderr %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "\nHome Run (hr) [stat]\n") {
		t.Errorf("selected stdout = %q", stdout.String())
	}
	if prompt.flushes != 1 {
		t.Errorf("prompt flush count = %d, want 1", prompt.flushes)
	}

	stdout.Reset()
	stderr.Reset()
	prompt = &recordingFlushWriter{Writer: &stderr}
	code = runWithContext([]string{"i", "run"}, commandContext{
		stdin:            strings.NewReader("0\n"),
		stdout:           &stdout,
		stderr:           &stderr,
		prompt:           prompt,
		stdinIsTerminal:  true,
		stderrIsTerminal: true,
	})
	if code != 1 || stdout.Len() != 0 || !strings.HasSuffix(stderr.String(), "i: invalid selection; enter a number from 1 through 3\n") {
		t.Errorf("invalid selection = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestInteractiveGlossaryStreamFailuresUseFrozenDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		prompt     flushWriter
		stdin      io.Reader
		wantStderr string
	}{
		{name: "HeadingWrite", prompt: &failWriteFlushWriter{failWrite: 1}, stdin: strings.NewReader("1\n"), wantStderr: "i: show glossary choices: write failed\n"},
		{name: "ChoiceWrite", prompt: &failWriteFlushWriter{failWrite: 2}, stdin: strings.NewReader("1\n"), wantStderr: "i: show glossary choices: write failed\n"},
		{name: "PromptWrite", prompt: &failWriteFlushWriter{failWrite: 5}, stdin: strings.NewReader("1\n"), wantStderr: "i: prompt for glossary selection: write failed\n"},
		{name: "PromptFlush", prompt: &failWriteFlushWriter{flushErr: errors.New("flush failed")}, stdin: strings.NewReader("1\n"), wantStderr: "i: prompt for glossary selection: write failed\n"},
		{name: "SelectionRead", prompt: &failWriteFlushWriter{}, stdin: failingReader{}, wantStderr: "i: read glossary selection: failed\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runWithContext([]string{"i", "run"}, commandContext{
				stdin:            test.stdin,
				stdout:           &stdout,
				stderr:           &stderr,
				prompt:           test.prompt,
				stdinIsTerminal:  true,
				stderrIsTerminal: true,
			})
			if code != 1 || stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Errorf("result = code %d, stdout %q, stderr %q; want stderr %q", code, stdout.String(), stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestUnportedCommandsRemainFailClosedWithGlossaryFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-d", "st"},
		{"st", "--debug"},
		{"--league", "mlb.l.1", "sync"},
		{"sync", "--league=mlb.l.1"},
	} {
		code, stdout, stderr := execute(args)
		if code != 2 || len(stdout) != 0 || string(stderr) != notImplementedMessage {
			t.Errorf("args %q = code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func execute(args []string) (int, []byte, []byte) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.Bytes()
}

func rootHelpGolden(t *testing.T) []byte {
	t.Helper()
	golden, err := os.ReadFile("testdata/root-help.txt")
	if err != nil {
		t.Fatalf("read root help fixture: %v", err)
	}
	return golden
}

type recordingFlushWriter struct {
	io.Writer
	flushes int
}

func (writer *recordingFlushWriter) Flush() error {
	writer.flushes++
	return nil
}

type failWriteFlushWriter struct {
	writes    int
	failWrite int
	flushErr  error
}

func (writer *failWriteFlushWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failWrite {
		return 0, errors.New("write failed")
	}
	return len(value), nil
}

func (writer *failWriteFlushWriter) Flush() error {
	return writer.flushErr
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
