// Package output keeps human-vs-JSON formatting in one place so every
// command can support `--json` for free without each one reinventing
// the indent / newline / pluralization rules.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Mode picks the output format. Default is "human" for an interactive
// TTY; commands flip to "json" when --json is passed or when stdout
// is a pipe in a CI workflow.
type Mode int

const (
	Human Mode = iota
	JSON
)

// JSON renders v as indented JSON to w.
func RenderJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Print is the per-command shim: pass it the human rendering function
// AND the data, and it picks based on mode. Keeps each command's
// output logic readable instead of branching on mode at every step.
func Print(w io.Writer, mode Mode, v any, human func(io.Writer, any) error) error {
	if mode == JSON {
		return RenderJSON(w, v)
	}
	return human(w, v)
}

// Stderrf is the standard "logging-style" message channel — everything
// that's not the actual data output (status messages, hints) goes to
// stderr so `outpost domain list --json | jq …` works cleanly.
func Stderrf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
	if format == "" || format[len(format)-1] != '\n' {
		fmt.Fprintln(os.Stderr)
	}
}
