package commands

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

func domainCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage local mail domains",
	}
	cmd.AddCommand(domainListCmd(flags))
	cmd.AddCommand(domainAddCmd(flags))
	cmd.AddCommand(domainRemoveCmd(flags))
	cmd.AddCommand(domainDNSCmd(flags))
	cmd.AddCommand(domainDkimRotateCmd(flags))
	return cmd
}

func domainDkimRotateCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "dkim-rotate <name>",
		Short: "Generate and activate a new DKIM selector",
		Long: `Mints a new DKIM selector for the domain and updates the configured
signing key. The previous selector is retired (kept on disk so
in-flight mail still verifies during the propagation window).

After rotation, publish the new public key in DNS — the TXT value is
in the response, or run ` + "`outpost domain dns template <name>`" + ` to
download a fresh zone fragment.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				id, err := resolveDomainID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/domains/%s/dkim-rotate", url.PathEscape(id)), nil, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "New selector: %s\n", asString(resp["newSelector"]))
				fmt.Fprintf(cmd.OutOrStdout(), "DNS TXT (publish under <selector>._domainkey.%s):\n%s\n",
					args[0], asString(resp["dnsTxtRecord"]))
				return nil
			})
		},
	}
}

func domainListCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured domains",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				var resp struct {
					Domains []map[string]any `json:"domains"`
				}
				if err := c.Do(ctx, "GET", "/admin/api/domains", nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp.Domains, renderDomainsHuman)
			})
		},
	}
}

func domainAddCmd(flags *globalFlags) *cobra.Command {
	var primary bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new local mail domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				body := map[string]any{
					"name":      args[0],
					"isPrimary": primary,
					"isLocal":   true,
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", "/admin/api/domains", body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				domain, _ := resp["domain"].(map[string]any)
				fmt.Fprintf(cmd.OutOrStdout(), "Created domain %s (id=%s)\n", asString(domain["name"]), asString(domain["id"]))
				output.Stderrf("Run `outpost domain dns template %s` to download the records to publish in DNS.", asString(domain["name"]))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&primary, "primary", false, "Mark as the primary domain")
	return cmd
}

func domainRemoveCmd(flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a local mail domain (refuses if any users or aliases reference it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to confirm deletion of %q", args[0])
			}
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				// First resolve name → id via list, since the API
				// keys domains by id.
				id, err := resolveDomainID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/domains/%s", url.PathEscape(id)), nil, nil); err != nil {
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

func domainDNSCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "DNS helpers (verify, template)",
	}
	cmd.AddCommand(domainDNSVerifyCmd(flags))
	cmd.AddCommand(domainDNSTemplateCmd(flags))
	return cmd
}

func domainDNSVerifyCmd(flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <name>",
		Short: "Re-run DNS verification for a domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				id, err := resolveDomainID(ctx, c, args[0])
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/domains/%s/dns/verify", url.PathEscape(id)), nil, &resp); err != nil {
					return err
				}
				return output.Print(cmd.OutOrStdout(), mode, resp, renderVerifyHuman)
			})
		},
	}
}

func domainDNSTemplateCmd(flags *globalFlags) *cobra.Command {
	var outFile string
	cmd := &cobra.Command{
		Use:   "template <name>",
		Short: "Download the DNS records template (BIND zone-file format)",
		Long: `Downloads the records to publish in DNS. The body is a portable BIND zone-file
fragment that imports cleanly into Cloudflare, Route 53, deSEC, BIND, etc.

Default output is stdout; use -o to write to a file directly.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				id, err := resolveDomainID(ctx, c, args[0])
				if err != nil {
					return err
				}
				body, _, err := c.DoRaw(ctx, "GET", fmt.Sprintf("/admin/api/domains/%s/dns/zonefile", url.PathEscape(id)))
				if err != nil {
					return err
				}
				if outFile == "" {
					_, err := cmd.OutOrStdout().Write(body)
					return err
				}
				if err := os.WriteFile(outFile, body, 0o600); err != nil {
					return err
				}
				output.Stderrf("Wrote %d bytes to %s", len(body), outFile)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Write the template to a file instead of stdout")
	return cmd
}

func resolveDomainID(ctx context.Context, c *client.Client, name string) (string, error) {
	var resp struct {
		Domains []map[string]any `json:"domains"`
	}
	if err := c.Do(ctx, "GET", "/admin/api/domains", nil, &resp); err != nil {
		return "", err
	}
	for _, d := range resp.Domains {
		if asString(d["name"]) == name {
			return asString(d["id"]), nil
		}
	}
	return "", fmt.Errorf("no domain named %q on this server", name)
}

func renderDomainsHuman(w io.Writer, v any) error {
	domains, _ := v.([]map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPRIMARY\tDNS\tDKIM\tTLS")
	for _, d := range domains {
		primary := "no"
		if b, _ := d["isPrimary"].(bool); b {
			primary = "yes"
		}
		dns := asString(d["dnsStatus"])
		dkim := ""
		if k, _ := d["dkim"].(map[string]any); k != nil {
			dkim = asString(k["activeSelector"])
			if dkim == "" {
				dkim = "(none)"
			}
		}
		tls := ""
		if t, _ := d["tlsProvisioning"].(map[string]any); t != nil {
			tls = asString(t["status"])
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", asString(d["name"]), primary, dns, dkim, tls)
	}
	return tw.Flush()
}

func renderVerifyHuman(w io.Writer, v any) error {
	payload, _ := v.(map[string]any)
	fmt.Fprintf(w, "Status: %s\n", asString(payload["status"]))
	records, _ := payload["records"].([]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RECORD\tTYPE\tVERDICT")
	for _, r := range records {
		rec, _ := r.(map[string]any)
		fmt.Fprintf(tw, "%s\t%s\t%s\n",
			asString(rec["name"]),
			asString(rec["type"]),
			asString(rec["verdict"]),
		)
	}
	return tw.Flush()
}
