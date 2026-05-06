// Package main is the entry point for the `outpost` CLI binary.
//
// outpost is a remote-administration tool for an Outpost mail server.
// It talks to the server's /admin/api and /jmap surfaces over HTTPS,
// authenticated with an API key minted via `mailctl key create` on the
// server. For on-server work (when SSH'd in), use `mailctl` instead —
// that's the direct-DB tool that works during recovery.
package main

import (
	"fmt"
	"os"

	"github.com/jasonbayton/outpost-cli/commands"
)

// version is overwritten at build time by `goreleaser` via -ldflags.
var version = "dev"

func main() {
	root := commands.NewRoot(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
