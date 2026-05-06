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

func healthCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Show server health (DB, queues, certs, reconciler)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var payload map[string]any
				if err := c.Do(ctx, "GET", "/admin/api/health", nil, &payload); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, payload, renderHealthHuman)
			})
		},
	}
}

func renderHealthHuman(w io.Writer, v any) error {
	payload, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "STATUS\t%s\n", asString(payload["status"]))
	fmt.Fprintf(tw, "CHECKED\t%s\n", asString(payload["checkedAt"]))
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "CHECK\tSTATE\tDETAIL")
	checks := map[string]string{
		"db":               "database",
		"blobDir":          "blob dir",
		"queueWorker":      "queue worker",
		"outboundQueue":    "outbound queue",
		"inboundSpool":     "inbound spool",
	}
	for key, label := range checks {
		check, _ := payload[key].(map[string]any)
		fmt.Fprintf(tw, "%s\t%s\t%s\n", label, asString(check["status"]), asString(check["error"]))
	}
	if rec, ok := payload["reconciler"].(map[string]any); ok {
		detail := asString(rec["error"])
		if detail == "" {
			if skipped, _ := rec["skippedUnresolved"].([]any); len(skipped) > 0 {
				detail = fmt.Sprintf("waiting DNS: %d", len(skipped))
			} else if checked, _ := rec["checkedAt"].(string); checked != "" {
				detail = fmt.Sprintf("last tick %s", checked)
			} else {
				detail = "no run yet"
			}
		}
		fmt.Fprintf(tw, "reconciler\t%s\t%s\n", asString(rec["status"]), detail)
	}
	if certs, ok := payload["certs"].([]any); ok {
		for _, c := range certs {
			cert, _ := c.(map[string]any)
			fmt.Fprintf(tw, "tls (%s)\t%s\texpires %s\n",
				asString(cert["listener"]),
				certStatus(cert),
				asString(cert["notAfter"]),
			)
		}
	}
	return tw.Flush()
}

func certStatus(cert map[string]any) string {
	days, _ := cert["daysUntilExpiry"].(float64)
	switch {
	case days < 14:
		return "down"
	case days < 30:
		return "degraded"
	default:
		return "ok"
	}
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}
