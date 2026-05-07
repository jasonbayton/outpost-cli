// Package commands wires up every cobra command. NewRoot is the only
// entry point exported back to main; commands are added here so the
// help text stays in a predictable order regardless of file layout.
package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/config"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

// global flags shared by every subcommand.
type globalFlags struct {
	server   string
	jsonOut  bool
	insecure bool          // future: skip TLS verify for self-signed dev hosts
	timeout  time.Duration // 0 means "use the client default"
}

// NewRoot constructs the root cobra command tree.
func NewRoot(version string) *cobra.Command {
	flags := &globalFlags{}
	root := &cobra.Command{
		Use:   "outpost",
		Short: "Remote admin CLI for an Outpost mail server",
		Long: `outpost talks to your Outpost server's /admin/api and /jmap surfaces over HTTPS.

For on-server work (when SSH'd in), use mailctl — that's the direct-DB tool that
keeps working during recovery. outpost is the laptop / CI tool.`,
		Version: version,
		SilenceUsage: true, // a returned error is rarely a misuse — don't dump usage
	}
	root.PersistentFlags().StringVar(&flags.server, "server", "", "Server hostname (overrides default; falls back to $OUTPOST_SERVER and ~/.config/outpost/config.toml)")
	root.PersistentFlags().BoolVar(&flags.jsonOut, "json", false, "Emit JSON instead of human-readable output")
	root.PersistentFlags().DurationVar(&flags.timeout, "timeout", 0, "HTTP timeout per request (default 30s; raise for large mail attachments)")

	root.AddCommand(authCmd(flags))
	root.AddCommand(healthCmd(flags))
	root.AddCommand(domainCmd(flags))
	root.AddCommand(reconcileCmd(flags))
	root.AddCommand(mailCmd(flags))

	return root
}

// withClient is the standard prologue for every command that needs
// to talk to the server. Loads config, resolves the host, builds a
// client, and hands it to the body. Keeps each command from
// duplicating the same six lines of plumbing.
func withClient(flags *globalFlags, body func(ctx context.Context, c *client.Client, mode output.Mode) error) error {
	cfg, _, err := config.Load()
	if err != nil {
		return err
	}
	_, host, err := cfg.Resolve(flags.server)
	if err != nil {
		return err
	}
	c, err := client.NewWithTimeout(host, version(), flags.timeout)
	if err != nil {
		return err
	}
	mode := output.Human
	if flags.jsonOut {
		mode = output.JSON
	}
	ctx, cancel := context.WithCancel(rootContext())
	defer cancel()
	return body(ctx, c, mode)
}

// version returns the binary version. We thread it from main via the
// cobra cmd's Version field, but commands sometimes need it for
// User-Agent strings before that's reachable — so this fallback
// keeps the User-Agent stable until we wire a registry.
func version() string {
	if v := os.Getenv("OUTPOST_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// rootContext is the cancellation hook commands inherit from. We
// don't wire signal handling here yet — for v1, ctrl-C exits the
// process, which is the simple right thing for short-lived CLI
// invocations. A long-running `outpost mail tail` will need its own
// signal.NotifyContext when it lands.
func rootContext() context.Context { return context.Background() }

// fmtErr is shorthand for printing context to stderr. Most commands
// just `return err` and the cobra runner does the right thing, but
// occasionally a command needs to emit an action-suggestion line
// alongside the error.
func fmtErr(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}
