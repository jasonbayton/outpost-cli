package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

func quarantineCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Inspect and act on quarantined messages",
	}
	cmd.AddCommand(quarantineListCmd(flags))
	cmd.AddCommand(quarantineGetCmd(flags))
	cmd.AddCommand(quarantineReleaseCmd(flags))
	cmd.AddCommand(quarantineBlacklistCmd(flags))
	cmd.AddCommand(quarantineDeleteCmd(flags))
	return cmd
}

func quarantineListCmd(flags *globalFlags) *cobra.Command {
	var reason, domain, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List quarantined messages (filterable)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				q := url.Values{}
				if reason != "" {
					q.Set("reason", reason)
				}
				if domain != "" {
					q.Set("domain", domain)
				}
				if since != "" {
					q.Set("since", since)
				}
				if limit > 0 {
					q.Set("limit", fmt.Sprint(limit))
				}
				path := "/admin/api/quarantine"
				if encoded := q.Encode(); encoded != "" {
					path += "?" + encoded
				}
				var resp struct {
					Items []map[string]any `json:"items"`
					Total int              `json:"total"`
				}
				if err := c.Do(ctx, "GET", path, nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				return renderQuarantineRowsHuman(cmd.OutOrStdout(), resp.Items)
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Filter by quarantine reason")
	cmd.Flags().StringVar(&domain, "domain", "", "Filter by sender domain")
	cmd.Flags().StringVar(&since, "since", "", "ISO-8601 timestamp; only messages after this point")
	cmd.Flags().IntVar(&limit, "limit", 0, "Cap the number of rows returned (server may apply its own cap too)")
	return cmd
}

func quarantineGetCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <email-id>",
		Short: "Show one quarantined message in detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var resp map[string]any
				if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/quarantine/%s", url.PathEscape(args[0])), nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp, renderQuarantineDetailHuman)
			})
		},
	}
}

func quarantineReleaseCmd(flags *globalFlags) *cobra.Command {
	var whitelist bool
	cmd := &cobra.Command{
		Use:   "release <email-id>",
		Short: "Release a quarantined message back into the user's mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				path := fmt.Sprintf("/admin/api/quarantine/%s/release", url.PathEscape(args[0]))
				if whitelist {
					path = fmt.Sprintf("/admin/api/quarantine/%s/release-whitelist", url.PathEscape(args[0]))
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", path, nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				output.Stderrf("Released %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&whitelist, "whitelist", false, "Also add the sender domain to the allow-list")
	return cmd
}

func quarantineBlacklistCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "blacklist <email-id>",
		Short: "Deny-list the message's sender domain and delete every quarantined message from it",
		Long: `Destructive: this both writes a deny-list entry for the sender's
domain (so future inbound from that domain is rejected) AND deletes
every currently-quarantined message from that domain. Pass --yes to
confirm.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm blacklisting; this also deletes every quarantined message from the sender's domain")
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/quarantine/%s/blacklist-sender", url.PathEscape(args[0])), nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Blacklisted %s; deleted %v message(s)\n",
					asString(resp["domain"]), resp["affected"])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm blacklist + bulk delete")
	return cmd
}

func quarantineDeleteCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <email-id>",
		Short: "Permanently delete a quarantined message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm deletion of %q", args[0])
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/quarantine/%s", url.PathEscape(args[0])), nil, nil); err != nil {
					return err
				}
				output.Stderrf("Deleted %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm deletion")
	return cmd
}

func renderQuarantineRowsHuman(w io.Writer, items []map[string]any) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "(no quarantined messages)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFROM\tREASON\tRECEIVED")
	for _, m := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			asString(m["id"]),
			asString(m["from"]),
			asString(m["reason"]),
			asString(m["receivedAt"]),
		)
	}
	return tw.Flush()
}

func renderQuarantineDetailHuman(w io.Writer, v any) error {
	m, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, k := range []string{"id", "from", "to", "subject", "reason", "receivedAt", "size"} {
		fmt.Fprintf(tw, "%s\t%v\n", k, m[k])
	}
	return tw.Flush()
}
