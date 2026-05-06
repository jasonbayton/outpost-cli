package commands

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

func reconcileCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Domain reconciler — keep nginx + Let's Encrypt aligned with the domains table",
	}
	cmd.AddCommand(reconcileStatusCmd(flags))
	return cmd
}

func reconcileStatusCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the latest reconcile-tick result",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				// Reconciler state is exposed as a sub-field of the
				// /health payload (see admin_panel._reconciler_summary)
				// rather than a dedicated endpoint, since the same
				// data also drives the Health screen card.
				var payload map[string]any
				if err := c.Do(ctx, "GET", "/admin/api/health", nil, &payload); err != nil {
					return err
				}
				rec, _ := payload["reconciler"].(map[string]any)
				if rec == nil {
					return fmt.Errorf("server response did not include reconciler state")
				}
				return output.Print(cmd.OutOrStdout(), mode, rec, renderReconcilerHuman)
			})
		},
	}
}

func renderReconcilerHuman(w io.Writer, v any) error {
	rec, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Status\t%s\n", asString(rec["status"]))
	if checkedAt := asString(rec["checkedAt"]); checkedAt != "" {
		fmt.Fprintf(tw, "Last tick\t%s\n", checkedAt)
	} else {
		fmt.Fprintf(tw, "Last tick\t(no run yet)\n")
	}
	if covered, _ := rec["domainsCovered"].(float64); covered > 0 {
		fmt.Fprintf(tw, "Domains covered\t%d\n", int(covered))
	}
	if reloaded, _ := rec["nginxReloaded"].(bool); reloaded {
		fmt.Fprintf(tw, "Nginx\treloaded\n")
	}
	if errStr := asString(rec["error"]); errStr != "" {
		fmt.Fprintf(tw, "Error\t%s\n", errStr)
	}
	if skipped, _ := rec["skippedUnresolved"].([]any); len(skipped) > 0 {
		names := make([]string, 0, len(skipped))
		for _, s := range skipped {
			names = append(names, asString(s))
		}
		fmt.Fprintf(tw, "Waiting DNS\t%v\n", names)
	}
	if added, _ := rec["sansAdded"].([]any); len(added) > 0 {
		names := make([]string, 0, len(added))
		for _, s := range added {
			names = append(names, asString(s))
		}
		fmt.Fprintf(tw, "Added SANs\t%v\n", names)
	}
	return tw.Flush()
}
