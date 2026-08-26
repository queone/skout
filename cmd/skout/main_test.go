package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/queone/skout/internal/cli"
)

func TestProductionVersionAndRootHelpWiring(t *testing.T) {
	for _, test := range []struct {
		args    []string
		fixture string
		want    string
	}{
		{args: nil, fixture: "testdata/root-help.txt"},
		{args: []string{"--help"}, fixture: "testdata/root-help.txt"},
		{args: []string{"--version"}, want: "skout " + programVersion + "\n"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(test.args, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) code = %d, stderr %q", test.args, code, stderr.String())
		}
		want := test.want
		if test.fixture != "" {
			data, err := os.ReadFile(test.fixture)
			if err != nil {
				t.Fatal(err)
			}
			want = strings.Replace(string(data), "{{VERSION}}", programVersion, 1)
		}
		if stdout.String() != want || stderr.Len() != 0 {
			t.Errorf("run(%v) stdout=%q stderr=%q", test.args, stdout.String(), stderr.String())
		}
	}
}

func TestProductionFantasyAndResetWiringHasNoDeferredCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"reset"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "reset: runtime is unavailable; reinstall skout\n" {
		t.Fatalf("reset code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for command, diagnostic := range map[string]string{"m": "match", "r": "roster", "rt": "roster totals", "h": "player", "p": "player"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{command}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), diagnostic+": runtime is unavailable") {
			t.Errorf("%s code=%d stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
		}
	}
	handlers := cli.ProductionHandlers(programVersion, cli.Context{})
	if handlers.Reset == nil || handlers.Matchup == nil || handlers.Roster == nil || handlers.RosterTotals == nil || handlers.Hitters == nil || handlers.Pitchers == nil {
		t.Fatal("one or more production handlers are unwired")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sync", "-l", "mlb.l.1", "-T", "Operators"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "sync: runtime is unavailable; reinstall skout\n" {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestProductionResetUsesInjectedProcessStreamsAndExactLocalBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	databasePath := filepath.Join(home, ".config", "skout", "skout.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if err := os.WriteFile(path, []byte("delete"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	preserved := filepath.Join(filepath.Dir(databasePath), "config.json")
	if err := os.WriteFile(preserved, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	context := cli.Context{Stdin: strings.NewReader("yes\n"), Stdout: &stdout, Stderr: &stderr}
	code := cli.Run([]string{"reset"}, programVersion, context, cli.ProductionHandlers(programVersion, context))
	want := "This will delete " + databasePath + " and require a full re-sync.\nContinue? [y/N] Database deleted. Run skout sync to rebuild.\n"
	if code != 0 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("reset target %s remains: %v", path, err)
		}
	}
	if data, err := os.ReadFile(preserved); err != nil || string(data) != "keep" {
		t.Fatalf("preserved config data=%q err=%v", data, err)
	}
}

func TestDocumentationDescribesCompleteExecutableSurfaceAndRemainingPlan(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	readme := read("../../README.md")
	architecture := read("../../arch.md")
	plan := read("../../plan.md")
	for _, command := range []string{"skout reset", "skout m", "skout r", "skout rt", "skout h", "skout p"} {
		if !strings.Contains(readme, command) {
			t.Errorf("README missing %q", command)
		}
	}
	if !strings.Contains(architecture, "Rust reference repository is already archived") || !strings.Contains(architecture, "not a runtime or release dependency") || strings.Contains(architecture, "Only the final cross-repository parity review remains") {
		t.Errorf("architecture migration boundary is inaccurate")
	}
	if strings.Count(plan, "## ") != 2 || !strings.Contains(plan, "## Product Direction") || !strings.Contains(plan, "## Ideas To Explore") || strings.Contains(plan, "IE1") || strings.Contains(plan, "IE2") || strings.Contains(plan, "IE3") || strings.Contains(plan, "IE4") {
		t.Errorf("plan structure or delivered ideas are inaccurate: %q", plan)
	}
}
