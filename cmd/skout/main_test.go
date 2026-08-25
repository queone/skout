package main

import (
	"bytes"
	"os"
	"testing"
)

func TestProductionVersionAndRootHelpWiring(t *testing.T) {
	for _, test := range []struct {
		args    []string
		fixture string
		want    string
	}{
		{args: nil, fixture: "testdata/root-help.txt"},
		{args: []string{"--help"}, fixture: "testdata/root-help.txt"},
		{args: []string{"--version"}, want: "skout 0.3.1\n"},
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

func TestProductionDeferredWiringFailsClosed(t *testing.T) {
	for _, command := range []string{"reset", "sync", "m", "r", "rt", "h", "p"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command}, &stdout, &stderr); code != 2 {
			t.Errorf("%s code=%d", command, code)
		}
		if stdout.Len() != 0 || stderr.String() != "skout: command not implemented in this migration slice\n" {
			t.Errorf("%s stdout=%q stderr=%q", command, stdout.String(), stderr.String())
		}
	}
}
