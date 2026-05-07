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

func mailboxCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mailbox",
		Short: "Manage shared mailboxes",
		Long: `Shared mailboxes are non-personal accounts that one or more users have
delegated access to (postmaster, support, sales, etc.). Differs from a
regular user account in that there's no human owner — assignees are
listed via --assign / --unassign.`,
	}
	cmd.AddCommand(mailboxListCmd(flags))
	cmd.AddCommand(mailboxGetCmd(flags))
	cmd.AddCommand(mailboxAddCmd(flags))
	cmd.AddCommand(mailboxUpdateCmd(flags))
	cmd.AddCommand(mailboxDeactivateCmd(flags))
	cmd.AddCommand(mailboxDeleteCmd(flags))
	cmd.AddCommand(mailboxKeyCmd(flags))
	return cmd
}

func mailboxListCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List shared mailboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var resp struct {
					Mailboxes []map[string]any `json:"mailboxes"`
				}
				if err := c.Do(ctx, "GET", "/admin/api/mailboxes", nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp.Mailboxes, renderMailboxesHuman)
			})
		},
	}
}

func mailboxGetCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <address>",
		Short: "Show one shared mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/mailboxes/%s", url.PathEscape(accountID)), nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp, renderMailboxDetailHuman)
			})
		},
	}
}

func mailboxAddCmd(flags *globalFlags) *cobra.Command {
	var displayName string
	var quotaBytes int64
	var outboundOnly bool
	var aliases, assignees []string
	cmd := &cobra.Command{
		Use:   "add <address>",
		Short: "Create a shared mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				localPart, domainName, err := splitEmail(args[0])
				if err != nil {
					return err
				}
				domainID, err := resolveDomainID(ctx, c, domainName)
				if err != nil {
					return err
				}
				body := map[string]any{
					"localPart":    localPart,
					"domainId":     domainID,
					"displayName":  displayName,
					"outboundOnly": outboundOnly,
				}
				if quotaBytes > 0 {
					body["quotaBytes"] = quotaBytes
				}
				if len(aliases) > 0 {
					body["aliases"] = aliases
				}
				if len(assignees) > 0 {
					list, err := buildAssignees(ctx, c, assignees)
					if err != nil {
						return err
					}
					body["assignees"] = list
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", "/admin/api/mailboxes", body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Created shared mailbox %s (id=%s)\n", args[0], asString(resp["id"]))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&displayName, "display", "", "Display name for the mailbox")
	cmd.Flags().Int64Var(&quotaBytes, "quota-bytes", 0, "Quota in bytes")
	cmd.Flags().BoolVar(&outboundOnly, "outbound-only", false, "Refuse inbound mail")
	cmd.Flags().StringArrayVar(&aliases, "alias", nil, "Alias address (repeatable)")
	cmd.Flags().StringArrayVar(&assignees, "assign", nil, "Assignment in user@example.com:viewer|responder|manager form (repeatable)")
	_ = cmd.MarkFlagRequired("display")
	return cmd
}

func mailboxUpdateCmd(flags *globalFlags) *cobra.Command {
	var displayName string
	var quotaBytes int64
	var outboundOnly, inboundOK bool
	var aliases, assignees []string
	cmd := &cobra.Command{
		Use:   "update <address>",
		Short: "Update a shared mailbox's display, quota, aliases, or assignees",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				localPart, domainName, err := splitEmail(args[0])
				if err != nil {
					return err
				}
				domainID, err := resolveDomainID(ctx, c, domainName)
				if err != nil {
					return err
				}
				body := map[string]any{
					"localPart":   localPart,
					"domainId":    domainID,
					"displayName": displayName,
				}
				if cmd.Flags().Changed("quota-bytes") {
					body["quotaBytes"] = quotaBytes
				}
				if outboundOnly {
					body["outboundOnly"] = true
				}
				if inboundOK {
					body["outboundOnly"] = false
				}
				if cmd.Flags().Changed("alias") {
					body["aliases"] = aliases
				}
				if cmd.Flags().Changed("assign") {
					list, err := buildAssignees(ctx, c, assignees)
					if err != nil {
						return err
					}
					body["assignees"] = list
				}
				var resp map[string]any
				if err := c.Do(ctx, "PATCH", fmt.Sprintf("/admin/api/mailboxes/%s", url.PathEscape(accountID)), body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				output.Stderrf("Updated %s", args[0])
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&displayName, "display", "", "New display name")
	cmd.Flags().Int64Var(&quotaBytes, "quota-bytes", 0, "Set quota in bytes")
	cmd.Flags().BoolVar(&outboundOnly, "outbound-only", false, "Refuse inbound")
	cmd.Flags().BoolVar(&inboundOK, "inbound", false, "Re-enable inbound")
	cmd.Flags().StringArrayVar(&aliases, "alias", nil, "Replace alias list (repeatable)")
	cmd.Flags().StringArrayVar(&assignees, "assign", nil, "Replace assignee list (repeatable)")
	_ = cmd.MarkFlagRequired("display")
	return cmd
}

func mailboxDeactivateCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "deactivate <address>",
		Short: "Deactivate a shared mailbox (soft-disable; mail to it returns 550)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/mailboxes/%s/deactivate", url.PathEscape(accountID)), nil, nil); err != nil {
					return err
				}
				output.Stderrf("Deactivated %s", args[0])
				return nil
			})
		},
	}
}

func mailboxDeleteCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <address>",
		Short: "Permanently delete a shared mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm deletion of %q", args[0])
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/mailboxes/%s", url.PathEscape(accountID)), nil, nil); err != nil {
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

func mailboxKeyCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage API keys scoped to a shared mailbox",
	}
	cmd.AddCommand(mailboxKeyCreateCmd(flags))
	cmd.AddCommand(mailboxKeyListCmd(flags))
	cmd.AddCommand(mailboxKeyRevokeCmd(flags))
	return cmd
}

func mailboxKeyCreateCmd(flags *globalFlags) *cobra.Command {
	var label string
	var scopes []string
	cmd := &cobra.Command{
		Use:   "create <address>",
		Short: "Mint an API key bound to a shared mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				body := map[string]any{
					"label":  label,
					"scopes": scopes,
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/mailboxes/%s/keys", url.PathEscape(accountID)), body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Token: %s\n", asString(resp["token"]))
				output.Stderrf("Save this token now — it will not be shown again.")
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Human label for the key (e.g. 'github-actions')")
	cmd.Flags().StringArrayVar(&scopes, "scope", []string{"jmap"}, "Scope to grant (repeatable; default: jmap)")
	_ = cmd.MarkFlagRequired("label")
	return cmd
}

func mailboxKeyListCmd(flags *globalFlags) *cobra.Command {
	var includeRevoked bool
	cmd := &cobra.Command{
		Use:   "list <address>",
		Short: "List API keys for a shared mailbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				path := fmt.Sprintf("/admin/api/mailboxes/%s/keys", url.PathEscape(accountID))
				if includeRevoked {
					path += "?include_revoked=true"
				}
				var resp struct {
					Keys []map[string]any `json:"keys"`
				}
				if err := c.Do(ctx, "GET", path, nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp.Keys)
				}
				return renderKeysHuman(cmd.OutOrStdout(), resp.Keys)
			})
		},
	}
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Include revoked keys")
	return cmd
}

func mailboxKeyRevokeCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <address> <key-id>",
		Short: "Revoke an API key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				accountID, err := resolveMailboxID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/mailboxes/%s/keys/%s", url.PathEscape(accountID), url.PathEscape(args[1])), nil, nil); err != nil {
					return err
				}
				output.Stderrf("Revoked %s on %s", args[1], args[0])
				return nil
			})
		},
	}
}

func resolveMailboxID(ctx context.Context, c *client.Client, address string) (string, error) {
	var resp struct {
		Mailboxes []map[string]any `json:"mailboxes"`
	}
	if err := c.Do(ctx, "GET", "/admin/api/mailboxes", nil, &resp); err != nil {
		return "", err
	}
	target := strings.TrimSpace(strings.ToLower(address))
	for _, m := range resp.Mailboxes {
		if strings.EqualFold(asString(m["address"]), target) || strings.EqualFold(asString(m["name"]), target) {
			return asString(m["id"]), nil
		}
	}
	return "", fmt.Errorf("no shared mailbox at %q", address)
}

func buildAssignees(ctx context.Context, c *client.Client, raw []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		userPart, preset, found := strings.Cut(entry, ":")
		if !found || strings.TrimSpace(preset) == "" {
			return nil, fmt.Errorf("--assign value %q must be 'user@example.com:viewer|responder|manager'", entry)
		}
		userID, err := resolveUserID(ctx, c, strings.TrimSpace(userPart))
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"userId": userID, "preset": strings.TrimSpace(preset)})
	}
	return out, nil
}

func renderMailboxesHuman(w io.Writer, v any) error {
	rows, _ := v.([]map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ADDRESS\tDISPLAY\tACTIVE\tASSIGNEES")
	for _, m := range rows {
		active := "yes"
		if a, ok := m["isActive"].(bool); ok && !a {
			active = "no"
		}
		assignees := ""
		if list, ok := m["assignees"].([]any); ok {
			parts := make([]string, 0, len(list))
			for _, a := range list {
				if mp, _ := a.(map[string]any); mp != nil {
					parts = append(parts, fmt.Sprintf("%s:%s", asString(mp["email"]), asString(mp["preset"])))
				}
			}
			assignees = strings.Join(parts, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			asString(m["address"]),
			asString(m["displayName"]),
			active,
			assignees,
		)
	}
	return tw.Flush()
}

func renderMailboxDetailHuman(w io.Writer, v any) error {
	m, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\n", asString(m["id"]))
	fmt.Fprintf(tw, "Address\t%s\n", asString(m["address"]))
	fmt.Fprintf(tw, "Display\t%s\n", asString(m["displayName"]))
	if quota, ok := m["quotaBytes"].(float64); ok {
		fmt.Fprintf(tw, "Quota bytes\t%d\n", int64(quota))
	}
	if list, ok := m["aliases"].([]any); ok && len(list) > 0 {
		names := make([]string, 0, len(list))
		for _, a := range list {
			names = append(names, asString(a))
		}
		fmt.Fprintf(tw, "Aliases\t%s\n", strings.Join(names, ", "))
	}
	if list, ok := m["assignees"].([]any); ok {
		for _, a := range list {
			if mp, _ := a.(map[string]any); mp != nil {
				fmt.Fprintf(tw, "Assignee\t%s (%s)\n", asString(mp["email"]), asString(mp["preset"]))
			}
		}
	}
	return tw.Flush()
}

func renderKeysHuman(w io.Writer, keys []map[string]any) error {
	if len(keys) == 0 {
		fmt.Fprintln(w, "(no keys)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tSCOPES\tCREATED\tREVOKED")
	for _, k := range keys {
		scopes := ""
		if list, ok := k["scopes"].([]any); ok {
			parts := make([]string, 0, len(list))
			for _, s := range list {
				parts = append(parts, asString(s))
			}
			scopes = strings.Join(parts, ",")
		}
		revoked := "no"
		if r, ok := k["revokedAt"].(string); ok && r != "" {
			revoked = r
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			asString(k["id"]),
			asString(k["label"]),
			scopes,
			asString(k["createdAt"]),
			revoked,
		)
	}
	return tw.Flush()
}
