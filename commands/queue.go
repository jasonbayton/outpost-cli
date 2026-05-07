package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

func queueCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect and act on the outbound delivery queue",
	}
	cmd.AddCommand(queueListCmd(flags))
	cmd.AddCommand(queueGetCmd(flags))
	cmd.AddCommand(queueRetryCmd(flags))
	cmd.AddCommand(queueHoldCmd(flags))
	cmd.AddCommand(queueReleaseCmd(flags))
	cmd.AddCommand(queueDeleteCmd(flags))
	return cmd
}

func queueListCmd(flags *globalFlags) *cobra.Command {
	var statuses []string
	var domain, since string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List outbound queue entries (filterable)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				q := url.Values{}
				// FastAPI's `list[str]` parses repeated query keys
				// (?status=a&status=b), NOT a comma-joined value.
				// Add() per element preserves that shape.
				for _, s := range statuses {
					if s != "" {
						q.Add("status", s)
					}
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
				path := "/admin/api/queue"
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
				return renderQueueRowsHuman(cmd.OutOrStdout(), resp.Items)
			})
		},
	}
	cmd.Flags().StringArrayVar(&statuses, "status", nil, "Filter by status (queued, sending, deferred, failed, done) — repeatable")
	cmd.Flags().StringVar(&domain, "domain", "", "Filter by recipient domain")
	cmd.Flags().StringVar(&since, "since", "", "ISO-8601 timestamp")
	cmd.Flags().IntVar(&limit, "limit", 0, "Cap the number of rows returned")
	return cmd
}

func queueGetCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <queue-id>",
		Short: "Show one queue entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var resp map[string]any
				if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/queue/%s", url.PathEscape(args[0])), nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp, renderQueueDetailHuman)
			})
		},
	}
}

func queueRetryCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <queue-id>",
		Short: "Mark a queued message for immediate redelivery",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return queuePostAction(cmd, flags, args[0], "retry")
		},
	}
}

func queueHoldCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "hold <queue-id>",
		Short: "Hold a queued message — delivery worker won't pick it up until released",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return queuePostAction(cmd, flags, args[0], "hold")
		},
	}
}

func queueReleaseCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "release <queue-id>",
		Short: "Release a held message back into the active queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return queuePostAction(cmd, flags, args[0], "release")
		},
	}
}

func queueDeleteCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <queue-id>",
		Short: "Permanently delete a queue entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm deletion of %q", args[0])
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/queue/%s", url.PathEscape(args[0])), nil, nil); err != nil {
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

func queuePostAction(cmd *cobra.Command, flags *globalFlags, queueID, action string) error {
	return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
		var resp map[string]any
		if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/queue/%s/%s", url.PathEscape(queueID), action), nil, &resp); err != nil {
			return err
		}
		if mode == output.JSON {
			return output.RenderJSON(cmd.OutOrStdout(), resp)
		}
		// strings.Title is deprecated since Go 1.18 (Unicode title-casing
		// behaviour issues); for our ASCII action verbs we just upper-case
		// the first byte, which is correct and stable.
		actionLabel := action
		if len(actionLabel) > 0 {
			actionLabel = strings.ToUpper(actionLabel[:1]) + actionLabel[1:]
		}
		output.Stderrf("%s %s", actionLabel, queueID)
		return nil
	})
}

func renderQueueRowsHuman(w io.Writer, items []map[string]any) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "(queue empty)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tFROM\tTO\tATTEMPTS\tNEXT")
	for _, m := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%v\t%s\n",
			asString(m["id"]),
			asString(m["status"]),
			asString(m["from"]),
			m["to"],
			m["attempts"],
			asString(m["nextAttemptAt"]),
		)
	}
	return tw.Flush()
}

func renderQueueDetailHuman(w io.Writer, v any) error {
	m, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, k := range []string{"id", "status", "from", "to", "subject", "attempts", "nextAttemptAt", "lastError", "createdAt"} {
		fmt.Fprintf(tw, "%s\t%v\n", k, m[k])
	}
	return tw.Flush()
}
