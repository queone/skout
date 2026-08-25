package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

const rootHelpSHA256 = "97c488fd8802973a673737d46a83fac67a89d7b66edfa226da0b26507b9204ad"

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
			if got, want := string(stdout), "skout 0.1.0\n"; got != want {
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
		{name: "Glossary", args: []string{"i"}},
		{name: "GlossaryAlias", args: []string{"whatis"}},
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
