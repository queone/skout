package main

import (
	"io"
	"os"
	"strings"

	"github.com/queone/skout/internal/cli"
	"github.com/queone/skout/internal/terminal"
)

const programVersion = "0.3.0"

func main() {
	context := cli.Context{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Prompt: os.Stderr,
		StdinIsTerminal: terminal.IsTerminal(os.Stdin), StdoutIsTerminal: terminal.IsTerminal(os.Stdout), StderrIsTerminal: terminal.IsTerminal(os.Stderr),
		NoColor: os.Getenv("NO_COLOR"), Term: os.Getenv("TERM"), ColorTerm: os.Getenv("COLORTERM"),
	}
	os.Exit(cli.Run(os.Args[1:], programVersion, context, cli.ProductionHandlers(programVersion, context)))
}

func run(args []string, stdout, stderr io.Writer) int {
	context := cli.Context{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr, Prompt: stderr}
	return cli.Run(args, programVersion, context, cli.Handlers{})
}
