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

func aliasCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage address aliases",
	}
	cmd.AddCommand(aliasListCmd(flags))
	cmd.AddCommand(aliasAddCmd(flags))
	cmd.AddCommand(aliasRemoveCmd(flags))
	return cmd
}

func aliasListCmd(flags *globalFlags) *cobra.Command {
	var domainFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List aliases (optionally filtered to one domain)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				// Server-side route lists aliases per-domain. Walk all
				// domains (or just the filtered one) and merge.
				domains, err := listDomains(ctx, c)
				if err != nil {
					return err
				}
				type aliasRow map[string]any
				var rows []aliasRow
				for _, d := range domains {
					name := asString(d["name"])
					if domainFilter != "" && !strings.EqualFold(domainFilter, name) {
						continue
					}
					id := asString(d["id"])
					var resp struct {
						Aliases []map[string]any `json:"aliases"`
					}
					if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/domains/%s/aliases", url.PathEscape(id)), nil, &resp); err != nil {
						return err
					}
					for _, a := range resp.Aliases {
						a["_domainName"] = name
						rows = append(rows, a)
					}
				}
				return output.Print(cmd.OutOrStdout(), mode, rows, renderAliasesHuman)
			})
		},
	}
	cmd.Flags().StringVar(&domainFilter, "domain", "", "List aliases for one domain only")
	return cmd
}

func aliasAddCmd(flags *globalFlags) *cobra.Command {
	var catchAll bool
	cmd := &cobra.Command{
		Use:   "add <source> <target-email>",
		Short: "Create an alias (catch-all if --catch-all is set)",
		Long: `Source addresses:
  alice@bayton.org              local-part alias on the given domain
  bayton.org   --catch-all      catch-all on the domain
  @bayton.org  --catch-all      same, with explicit @ prefix

Target must be an existing local user.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				source := strings.TrimSpace(strings.ToLower(args[0]))
				target := strings.TrimSpace(strings.ToLower(args[1]))
				var domainName, localPart string
				if catchAll {
					domainName = strings.TrimPrefix(source, "@")
				} else {
					if !strings.Contains(source, "@") {
						return fmt.Errorf("source must be local-part@domain (or use --catch-all with a bare domain)")
					}
					var err error
					localPart, domainName, err = splitEmail(source)
					if err != nil {
						return err
					}
				}
				domainID, err := resolveDomainID(ctx, c, domainName)
				if err != nil {
					return err
				}
				body := map[string]any{
					"targetAddress": target,
					"isCatchAll":    catchAll,
				}
				if !catchAll {
					body["localPart"] = localPart
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", fmt.Sprintf("/admin/api/domains/%s/aliases", url.PathEscape(domainID)), body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Created alias %s -> %s (id=%s)\n", source, target, asString(resp["id"]))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&catchAll, "catch-all", false, "Create a catch-all alias for the domain")
	return cmd
}

func aliasRemoveCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <id|address|@domain>",
		Short: "Delete one alias by id, full address, or @domain (catch-all)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				aliasID, err := resolveAliasID(ctx, c, args[0])
				if err != nil {
					return err
				}
				if err := c.Do(ctx, "DELETE", fmt.Sprintf("/admin/api/aliases/%s", url.PathEscape(aliasID)), nil, nil); err != nil {
					return err
				}
				output.Stderrf("Deleted alias %s", args[0])
				return nil
			})
		},
	}
	return cmd
}

func resolveAliasID(ctx context.Context, c *client.Client, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if strings.HasPrefix(identifier, "AA") && !strings.Contains(identifier, "@") {
		return identifier, nil
	}
	domains, err := listDomains(ctx, c)
	if err != nil {
		return "", err
	}
	idLower := strings.ToLower(identifier)
	wantCatchAll := strings.HasPrefix(idLower, "@")
	wantDomain := strings.TrimPrefix(idLower, "@")
	var wantLocal string
	if !wantCatchAll {
		if !strings.Contains(idLower, "@") {
			return "", fmt.Errorf("alias identifier must be an id (AA…), an @domain (catch-all), or local@domain")
		}
		l, d, err := splitEmail(idLower)
		if err != nil {
			return "", err
		}
		wantLocal = l
		wantDomain = d
	}
	for _, d := range domains {
		if !strings.EqualFold(asString(d["name"]), wantDomain) {
			continue
		}
		var resp struct {
			Aliases []map[string]any `json:"aliases"`
		}
		if err := c.Do(ctx, "GET", fmt.Sprintf("/admin/api/domains/%s/aliases", url.PathEscape(asString(d["id"]))), nil, &resp); err != nil {
			return "", err
		}
		for _, a := range resp.Aliases {
			isCatch, _ := a["isCatchAll"].(bool)
			if wantCatchAll && isCatch {
				return asString(a["id"]), nil
			}
			if !wantCatchAll && !isCatch && strings.EqualFold(asString(a["localPart"]), wantLocal) {
				return asString(a["id"]), nil
			}
		}
	}
	return "", fmt.Errorf("no alias matched %q", identifier)
}

func listDomains(ctx context.Context, c *client.Client) ([]map[string]any, error) {
	var resp struct {
		Domains []map[string]any `json:"domains"`
	}
	if err := c.Do(ctx, "GET", "/admin/api/domains", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Domains, nil
}

func renderAliasesHuman(w io.Writer, v any) error {
	rows, _ := v.([]map[string]any)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tTARGET\tID")
	for _, a := range rows {
		domainName := asString(a["_domainName"])
		var source string
		if catch, _ := a["isCatchAll"].(bool); catch {
			source = "@" + domainName
		} else {
			source = fmt.Sprintf("%s@%s", asString(a["localPart"]), domainName)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", source, asString(a["targetAddress"]), asString(a["id"]))
	}
	return tw.Flush()
}
