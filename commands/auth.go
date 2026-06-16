package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/config"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

func authCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage stored credentials",
	}
	cmd.AddCommand(authLoginCmd(flags))
	cmd.AddCommand(authStatusCmd(flags))
	cmd.AddCommand(authLogoutCmd(flags))
	return cmd
}

func authLoginCmd(flags *globalFlags) *cobra.Command {
	var serverFlag string
	var tokenStdin bool
	var setDefault bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store credentials for an Outpost server",
		Long: `Stores the API token used by every other command.

Get a token by SSHing to the server and running:

    mailctl key create <your-account-email> --label "laptop-cli" --scope jmap

Then paste the printed token here. The token is written to
$XDG_CONFIG_HOME/outpost/config.toml with mode 0600 — readable only by your
user. Use --set-default to make this server the one used when --server isn't
passed.

Tokens are NEVER accepted on the command line — putting a secret in argv
leaks it to shell history and /proc/$pid/cmdline. Provide it via:

  - the interactive prompt (default)
  - $OUTPOST_TOKEN env var
  - stdin pipe with --token-stdin (e.g. ` + "`echo $TOKEN | outpost auth login --server … --token-stdin`" + `)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			serverURL := strings.TrimSpace(serverFlag)
			if serverURL == "" {
				return errors.New("--server is required, e.g. --server https://outpost.example.org")
			}
			parsed, err := url.Parse(serverURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("invalid server URL %q", serverURL)
			}
			if parsed.Scheme != "https" && parsed.Scheme != "http" {
				return fmt.Errorf("server URL must use http(s), got %q", parsed.Scheme)
			}

			// Token sources, in priority order: --token-stdin, env, prompt.
			// We deliberately do NOT take a --token=value flag because that
			// puts the secret in argv (shell history, /proc).
			var token string
			switch {
			case tokenStdin:
				data, readErr := io.ReadAll(cmd.InOrStdin())
				if readErr != nil {
					return fmt.Errorf("read token from stdin: %w", readErr)
				}
				token = strings.TrimSpace(string(data))
			case os.Getenv("OUTPOST_TOKEN") != "":
				token = strings.TrimSpace(os.Getenv("OUTPOST_TOKEN"))
			default:
				token, err = readToken(cmd.InOrStdin(), cmd.OutOrStderr())
				if err != nil {
					return err
				}
			}
			if token == "" {
				return errors.New("no token provided")
			}

			// Probe the server with the token before persisting, so a
			// typo or stale key fails fast instead of silently storing
			// garbage that 401s on every later call.
			probeHost := config.HostConfig{URL: serverURL, Token: token}
			probe, err := client.New(probeHost, version())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(rootContext(), client.DefaultTimeout)
			defer cancel()
			var session map[string]any
			if err := probe.Do(ctx, "GET", "/auth/session", nil, &session); err != nil {
				if apiErr, ok := err.(*client.APIError); ok && apiErr.IsUnauthorized() {
					return fmt.Errorf("token rejected by %s — check it was pasted in full and the scope includes 'jmap'", serverURL)
				}
				return fmt.Errorf("could not reach %s: %w", serverURL, err)
			}

			hostKey := parsed.Host
			cfg.Hosts[hostKey] = config.HostConfig{URL: serverURL, Token: token}
			if setDefault || cfg.DefaultHost == "" {
				cfg.DefaultHost = hostKey
			}
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			output.Stderrf("Logged in as %s", coalesce(session, "username", "(unknown)"))
			output.Stderrf("Stored credentials for %s in %s", hostKey, path)
			if cfg.DefaultHost == hostKey {
				output.Stderrf("Default host set to %s", hostKey)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&serverFlag, "server", "", "Server URL, e.g. https://outpost.example.org")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "Read the token from stdin (use this when piping a token in CI)")
	cmd.Flags().BoolVar(&setDefault, "set-default", false, "Make this server the default for future invocations")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

func authStatusCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which server we're logged in to",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var session map[string]any
				if err := c.Do(ctx, "GET", "/auth/session", nil, &session); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), map[string]any{
						"server":  c.Base(),
						"session": session,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Server: %s\n", c.Base())
				fmt.Fprintf(cmd.OutOrStdout(), "User:   %s\n", coalesce(session, "username", "(unknown)"))
				return nil
			})
		},
	}
}

func authLogoutCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget stored credentials for the current (or --server) host",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _, err := config.Load()
			if err != nil {
				return err
			}
			target, _, err := cfg.Resolve(flags.server)
			if err != nil {
				return err
			}
			delete(cfg.Hosts, target)
			if cfg.DefaultHost == target {
				cfg.DefaultHost = ""
				for k := range cfg.Hosts {
					cfg.DefaultHost = k
					break
				}
			}
			if _, err := config.Save(cfg); err != nil {
				return err
			}
			output.Stderrf("Removed credentials for %s", target)
			return nil
		},
	}
}

// readToken reads from stdin (pipe or interactive prompt). When stdin is a
// terminal we disable echo so the pasted token never lands on screen or in a
// scrollback buffer. Non-TTY input (CI pipes) falls back to a plain line read.
func readToken(in io.Reader, prompt io.Writer) (string, error) {
	fmt.Fprint(prompt, "API token: ")
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt) // ReadPassword swallows the user's Enter newline
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func coalesce(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return fallback
}
