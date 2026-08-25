package main

import (
	"bytes"
	"os"
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
		{args: []string{"--version"}, want: "skout 0.5.0\n"},
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
			want = string(data)
		}
		if stdout.String() != want || stderr.Len() != 0 {
			t.Errorf("run(%v) stdout=%q stderr=%q", test.args, stdout.String(), stderr.String())
		}
	}
}

func TestProductionFantasyWiringAndSoleDeferredReset(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"reset"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != cli.DeferredMessage {
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
	if handlers.Matchup == nil || handlers.Roster == nil || handlers.RosterTotals == nil || handlers.Hitters == nil || handlers.Pitchers == nil {
		t.Fatal("one or more fantasy production handlers are unwired")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sync", "-l", "mlb.l.1", "-T", "Operators"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "sync: runtime is unavailable; reinstall skout\n" {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestFantasyDocumentationDescribesExecutableSurfaceAndRemainingPlan(t *testing.T) {
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
	for _, command := range []string{"skout m", "skout r", "skout rt", "skout h", "skout p"} {
		if !strings.Contains(readme, command) {
			t.Errorf("README missing %q", command)
		}
	}
	if !strings.Contains(architecture, "Rust reference repository is already archived") || !strings.Contains(architecture, "Only the final cross-repository parity review remains") {
		t.Errorf("architecture migration boundary is inaccurate")
	}
	if strings.Count(plan, "## ") != 2 || !strings.Contains(plan, "## Product Direction") || !strings.Contains(plan, "## Ideas To Explore") || strings.Contains(plan, "IE1") || strings.Contains(plan, "IE2") || strings.Contains(plan, "IE3") || !strings.Contains(plan, "IE4") {
		t.Errorf("plan structure or delivered ideas are inaccurate: %q", plan)
	}
}
