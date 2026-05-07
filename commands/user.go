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

func userCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage user accounts",
	}
	cmd.AddCommand(userListCmd(flags))
	cmd.AddCommand(userGetCmd(flags))
	cmd.AddCommand(userAddCmd(flags))
	cmd.AddCommand(userUpdateCmd(flags))
	cmd.AddCommand(userDisableCmd(flags))
	cmd.AddCommand(userDeleteCmd(flags))
	cmd.AddCommand(userResetPasswordCmd(flags))
	cmd.AddCommand(userResetPasskeyCmd(flags))
	return cmd
}

func userListCmd(flags *globalFlags) *cobra.Command {
	var includeDisabled bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List users",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				path := "/admin/api/users"
				if includeDisabled {
					path += "?include_disabled=true"
				}
				var resp struct {
					Users []map[string]any `json:"users"`
				}
				if err := c.Do(ctx, "GET", path, nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp.Users, renderUsersHuman)
			})
		},
	}
	cmd.Flags().BoolVar(&includeDisabled, "include-disabled", false, "Include deactivated users")
	return cmd
}

func userGetCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <email>",
		Short: "Show one user's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var detail map[string]any
				if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/users/%s", url.PathEscape(userID)), nil, &detail); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, detail, renderUserDetailHuman)
			})
		},
	}
}

func userAddCmd(flags *globalFlags) *cobra.Command {
	var displayName, password string
	var quotaBytes int64
	var admin, outboundOnly bool
	cmd := &cobra.Command{
		Use:   "add <email>",
		Short: "Create a new user (must be on a local domain)",
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
					"isAdmin":      admin,
					"outboundOnly": outboundOnly,
				}
				if quotaBytes > 0 {
					body["quotaBytes"] = quotaBytes
				}
				if password != "" {
					body["password"] = password
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", "/admin/api/users", body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				user, _ := resp["user"].(map[string]any)
				fmt.Fprintf(cmd.OutOrStdout(), "Created user %s (id=%s)\n", asString(user["email"]), asString(user["id"]))
				if temp, ok := resp["tempPassword"].(string); ok && temp != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Temporary password: %s\n", temp)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&displayName, "display", "", "Display name")
	cmd.Flags().StringVar(&password, "password", "", "Initial password (omitted = server generates one and prints it)")
	cmd.Flags().Int64Var(&quotaBytes, "quota-bytes", 0, "Mailbox quota in bytes (default 5GB)")
	cmd.Flags().BoolVar(&admin, "admin", false, "Create as an admin")
	cmd.Flags().BoolVar(&outboundOnly, "outbound-only", false, "Refuse inbound mail to this account (e.g. for a noreply@ identity)")
	return cmd
}

func userUpdateCmd(flags *globalFlags) *cobra.Command {
	var displayName string
	var quotaBytes int64
	var admin, noAdmin, outboundOnly, inboundOK, disable, enable bool
	cmd := &cobra.Command{
		Use:   "update <email>",
		Short: "Update a user's display name, role, quota, or inbound disposition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("display") {
				body["displayName"] = displayName
			}
			if admin {
				body["isAdmin"] = true
			}
			if noAdmin {
				body["isAdmin"] = false
			}
			if outboundOnly {
				body["outboundOnly"] = true
			}
			if inboundOK {
				body["outboundOnly"] = false
			}
			if disable {
				body["disabled"] = true
			}
			if enable {
				body["disabled"] = false
			}
			if cmd.Flags().Changed("quota-bytes") {
				body["quotaBytes"] = quotaBytes
			}
			if len(body) == 0 {
				return fmt.Errorf("no changes requested; pass at least one of --display / --admin / --no-admin / --quota-bytes / --outbound-only / --inbound / --disable / --enable")
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "PATCH", fmt.Sprintf("/admin/api/users/%s", url.PathEscape(userID)), body, &resp); err != nil {
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
	cmd.Flags().BoolVar(&admin, "admin", false, "Promote to admin")
	cmd.Flags().BoolVar(&noAdmin, "no-admin", false, "Demote from admin")
	cmd.Flags().BoolVar(&outboundOnly, "outbound-only", false, "Refuse inbound mail")
	cmd.Flags().BoolVar(&inboundOK, "inbound", false, "Re-enable inbound mail")
	cmd.Flags().BoolVar(&disable, "disable", false, "Deactivate the user (revokes sessions)")
	cmd.Flags().BoolVar(&enable, "enable", false, "Reactivate a previously disabled user")
	cmd.Flags().Int64Var(&quotaBytes, "quota-bytes", 0, "Set mailbox quota in bytes")
	return cmd
}

func userDisableCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <email>",
		Short: "Deactivate a user (shorthand for `user update --disable`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				body := map[string]any{"disabled": true}
				if err := c.Do(ctx, "PATCH", fmt.Sprintf("/admin/api/users/%s", url.PathEscape(userID)), body, nil); err != nil {
					return err
				}
				output.Stderrf("Disabled %s", args[0])
				return nil
			})
		},
	}
}

func userDeleteCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <email>",
		Short: "Delete a user (soft-deletes per the admin panel's grace period)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm deletion of %q", args[0])
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/users/%s", url.PathEscape(userID)), nil, nil); err != nil {
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

func userResetPasswordCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "reset-password <email>",
		Short: "Mint a new temporary password and revoke active sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/users/%s/reset-password", url.PathEscape(userID)), nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Temporary password: %s\n", asString(resp["tempPassword"]))
				return nil
			})
		},
	}
}

func userResetPasskeyCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "reset-passkey <email>",
		Short: "Wipe all passkeys and mint a one-time enrolment token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				userID, err := resolveUserID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/users/%s/reset-passkey", url.PathEscape(userID)), nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Enrolment token: %s\n", asString(resp["enrollmentToken"]))
				output.Stderrf("Have %s open the WebAuthn enrolment URL with this token within 10 minutes.", args[0])
				return nil
			})
		},
	}
}

// resolveUserID looks up a user by email or username via /users.
// Returns the User row id ("U..." prefix) the admin panel uses.
func resolveUserID(ctx context.Context, c *client.Client, identifier string) (string, error) {
	var resp struct {
		Users []map[string]any `json:"users"`
	}
	if err := c.Do(ctx, "GET", "/admin/api/users?include_disabled=true", nil, &resp); err != nil {
		return "", err
	}
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	for _, u := range resp.Users {
		if strings.EqualFold(asString(u["email"]), identifier) || strings.EqualFold(asString(u["username"]), identifier) {
			return asString(u["id"]), nil
		}
	}
	return "", fmt.Errorf("no user found for %q", identifier)
}

func splitEmail(addr string) (local, domain string, err error) {
	addr = strings.TrimSpace(strings.ToLower(addr))
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", "", fmt.Errorf("invalid email %q (expected local-part@domain)", addr)
	}
	return addr[:at], addr[at+1:], nil
}

func renderUsersHuman(w io.Writer, v any) error {
	users, _ := v.([]map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tROLE\tACTIVE\tDISPLAY")
	for _, u := range users {
		role := "user"
		if a, _ := u["isAdmin"].(bool); a {
			role = "admin"
		}
		active := "yes"
		if d, _ := u["disabled"].(bool); d {
			active = "no"
		} else if a, ok := u["isActive"].(bool); ok && !a {
			active = "no"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			asString(u["email"]),
			role,
			active,
			asString(u["displayName"]),
		)
	}
	return tw.Flush()
}

func renderUserDetailHuman(w io.Writer, v any) error {
	user, _ := v.(map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\n", asString(user["id"]))
	fmt.Fprintf(tw, "Email\t%s\n", asString(user["email"]))
	fmt.Fprintf(tw, "Username\t%s\n", asString(user["username"]))
	fmt.Fprintf(tw, "Display\t%s\n", asString(user["displayName"]))
	if a, _ := user["isAdmin"].(bool); a {
		fmt.Fprintf(tw, "Role\tadmin\n")
	} else {
		fmt.Fprintf(tw, "Role\tuser\n")
	}
	if quota, ok := user["quotaBytes"].(float64); ok {
		fmt.Fprintf(tw, "Quota bytes\t%d\n", int64(quota))
	}
	if d, _ := user["disabled"].(bool); d {
		fmt.Fprintf(tw, "Disabled\tyes\n")
	}
	if cnt, _ := user["aliasCount"].(float64); cnt > 0 {
		fmt.Fprintf(tw, "Aliases\t%d\n", int(cnt))
	}
	return tw.Flush()
}
