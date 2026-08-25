// Package providers contains bounded public-data adapters.
package providers

import "fmt"

func operationError(operation, step string, err error) error {
	return fmt.Errorf("%s: %s: %v; verify provider availability and retry", operation, step, err)
}

func invalid(operation, detail string) error {
	return fmt.Errorf("%s: %s; verify provider data and retry", operation, detail)
}

func bounded(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
