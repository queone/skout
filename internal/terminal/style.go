// Package terminal provides terminal-aware styling for the implemented CLI surface.
package terminal

import (
	"fmt"
	"os"
	"strings"
)

// ColorMode selects plain or ANSI-colored output.
type ColorMode uint8

const (
	// Plain emits text without ANSI escape sequences.
	Plain ColorMode = iota
	// Color emits the governed 256-color roles.
	Color
)

// ColorContext contains the terminal and environment evidence used for color selection.
type ColorContext struct {
	StdoutIsTerminal bool
	NoColor          string
	Term             string
	ColorTerm        string
}

// SelectColorMode resolves the output mode from terminal and environment evidence.
func SelectColorMode(context ColorContext) ColorMode {
	disabled := !context.StdoutIsTerminal || context.NoColor != "" || context.Term == "dumb"
	advertised := context.ColorTerm == "truecolor" || context.ColorTerm == "24bit" || strings.Contains(context.Term, "256color")
	if !disabled && advertised {
		return Color
	}
	return Plain
}

// IsTerminal reports whether file is attached to a character device.
func IsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Title styles the root-help title with the established bold bright-white role.
func Title(value string, mode ColorMode) string {
	return style(value, "1;38;5;231", mode)
}

// Subtitle styles the root-help subtitle with the established gray role.
func Subtitle(value string, mode ColorMode) string {
	return style(value, "38;5;245", mode)
}

// Section styles root-help section headings with the established white role.
func Section(value string, mode ColorMode) string {
	return style(value, "38;5;255", mode)
}

// Usage styles a help usage label with the governed bold-white role.
func Usage(value string, mode ColorMode) string {
	return style(value, "1;38;5;231", mode)
}

// Heading styles glossary class and entry headings with the governed blue role.
func Heading(value string, mode ColorMode) string {
	return style(value, "38;5;33", mode)
}

// Alias styles glossary alias lines with the governed gray role.
func Alias(value string, mode ColorMode) string {
	return style(value, "38;5;245", mode)
}

// TableHeading styles MLB display headings with the established blue role.
func TableHeading(value string, mode ColorMode) string {
	return style(value, "38;5;33", mode)
}

// Dim styles secondary MLB context with the shared gray role.
func Dim(value string, mode ColorMode) string {
	return style(value, "38;5;245", mode)
}

// Bold styles emphasized values with the shared bold role.
func Bold(value string, mode ColorMode) string {
	return style(value, "1", mode)
}

// Inverse renders a value in reverse video while preserving surrounding color.
func Inverse(value string, mode ColorMode) string {
	if mode != Color {
		return value
	}
	return "\x1b[7m" + value + "\x1b[27m"
}

// Good styles favorable MLB context with the shared green role.
func Good(value string, mode ColorMode) string {
	return style(value, "38;5;34", mode)
}

// Warning styles warnings and current-roster context with the shared yellow role.
func Warning(value string, mode ColorMode) string {
	return style(value, "38;5;100", mode)
}

// Injury styles unavailable status with the shared red role.
func Injury(value string, mode ColorMode) string {
	return style(value, "38;5;196", mode)
}

// LineupIndicator styles active or subdued favorable and unfavorable markers.
func LineupIndicator(value string, favorable, subdued bool, mode ColorMode) string {
	if mode != Color {
		return value
	}
	role := "38;5;196"
	if favorable {
		role = "38;5;46"
	}
	restore := "\x1b[0m"
	if subdued {
		role = "38;5;124"
		if favorable {
			role = "38;5;34"
		}
		restore = "\x1b[38;5;245m"
	}
	return fmt.Sprintf("\x1b[%sm%s%s", role, value, restore)
}

// RosterRow applies the active, injured-list, or off-active semantic tier.
func RosterRow(value, status string, mode ColorMode) string {
	if strings.HasPrefix(status, "D") {
		return Warning(value, mode)
	}
	if status != "" && status != "A" {
		return Dim(value, mode)
	}
	return value
}

// VisibleWidth returns the printable width of ANSI-styled text.
func VisibleWidth(value string) int {
	escape := false
	width := 0
	for _, character := range value {
		if character == '\x1b' {
			escape = true
			continue
		}
		if escape {
			if character == 'm' {
				escape = false
			}
			continue
		}
		width++
	}
	return width
}

func style(value, role string, mode ColorMode) string {
	if mode != Color {
		return value
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", role, value)
}
