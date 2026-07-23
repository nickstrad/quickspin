// Package greeting holds the core scaffolding logic for quickspin.
package greeting

import "fmt"

// Greet returns a greeting for the given name. An empty name falls back to
// "World".
func Greet(name string) string {
	if name == "" {
		name = "World"
	}
	return fmt.Sprintf("Hello, %s!", name)
}
