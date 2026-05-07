package commands

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jasonbayton/outpost-cli/internal/client"
	"github.com/jasonbayton/outpost-cli/internal/output"
)

// sendFlags is the full set of options for `outpost mail send`.
// Mirrors the SendRequest pydantic model on the server (see
// app.py::SendRequest), which is itself Resend-shaped, so the CLI
// is essentially a flag-to-JSON translator.
type sendFlags struct {
	from     string
	fromName string
	to       []string
	cc       []string
	bcc      []string
	replyTo  []string
	subject  string
	text     string
	textFile string
	html     string
	htmlFile string
	headers  []string
	attach   []string
}

func mailCmd(flags *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Send and inspect mail through the server",
	}
	cmd.AddCommand(mailSendCmd(flags))
	return cmd
}

func mailSendCmd(flags *globalFlags) *cobra.Command {
	sf := &sendFlags{}
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a single message via /mail/send",
		Long: `Sends one message authenticated with the stored API key.

Recipients accept either a bare address (jane@example.com) or a
"Display Name <jane@example.com>" form.

The body is taken from --text / --html (literal strings) or --text-file /
--html-file (read from disk). At least one of text or html is required.
Attachments are read from disk and base64-encoded inline; one --attach
flag per file.

Example:

    outpost mail send \
      --from postmaster@bayton.org \
      --to jason@example.com \
      --subject "Probe" \
      --text "Hello from the outpost CLI" \
      --attach summary.pdf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(flags, func(ctx context.Context, c *client.Client, mode output.Mode) error {
				body, err := buildSendBody(sf)
				if err != nil {
					return err
				}
				var resp map[string]any
				if err := c.Do(ctx, "POST", "/mail/send", body, &resp); err != nil {
					return err
				}
				if mode == output.JSON {
					return output.RenderJSON(cmd.OutOrStdout(), resp)
				}
				queueID := asString(resp["queueId"])
				if queueID == "" {
					queueID = "(unknown)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Queued: %s\n", queueID)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&sf.from, "from", "f", "", "From address (must be an identity owned by the API key's account)")
	cmd.Flags().StringVar(&sf.fromName, "from-name", "", "Optional display name for the From")
	cmd.Flags().StringArrayVarP(&sf.to, "to", "t", nil, "Recipient (repeatable)")
	cmd.Flags().StringArrayVar(&sf.cc, "cc", nil, "Cc recipient (repeatable)")
	cmd.Flags().StringArrayVar(&sf.bcc, "bcc", nil, "Bcc recipient (repeatable)")
	cmd.Flags().StringArrayVar(&sf.replyTo, "reply-to", nil, "Reply-To recipient (repeatable)")
	cmd.Flags().StringVarP(&sf.subject, "subject", "s", "", "Subject line")
	cmd.Flags().StringVar(&sf.text, "text", "", "Plain-text body (literal string)")
	cmd.Flags().StringVar(&sf.textFile, "text-file", "", "Plain-text body read from a file (use - for stdin)")
	cmd.Flags().StringVar(&sf.html, "html", "", "HTML body (literal string)")
	cmd.Flags().StringVar(&sf.htmlFile, "html-file", "", "HTML body read from a file (use - for stdin)")
	cmd.Flags().StringArrayVar(&sf.headers, "header", nil, "Extra header in 'Name: value' form (repeatable)")
	cmd.Flags().StringArrayVar(&sf.attach, "attach", nil, "Path to a file to attach (repeatable)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("subject")
	return cmd
}

// buildSendBody assembles the JSON body the server expects.
// Validation is intentionally light here — the server runs the
// same checks via pydantic, and any duplication would drift. We
// only catch what would otherwise look like a CLI typo (no body,
// missing recipients, unreadable attachment).
func buildSendBody(sf *sendFlags) (map[string]any, error) {
	if len(sf.to) == 0 {
		return nil, errors.New("at least one --to recipient is required")
	}
	body := map[string]any{
		"from":    sf.from,
		"to":      parseRecipients(sf.to),
		"subject": sf.subject,
	}
	if sf.fromName != "" {
		body["fromName"] = sf.fromName
	}
	if len(sf.cc) > 0 {
		body["cc"] = parseRecipients(sf.cc)
	}
	if len(sf.bcc) > 0 {
		body["bcc"] = parseRecipients(sf.bcc)
	}
	if len(sf.replyTo) > 0 {
		body["replyTo"] = parseRecipients(sf.replyTo)
	}

	text, err := pickBody(sf.text, sf.textFile)
	if err != nil {
		return nil, fmt.Errorf("text body: %w", err)
	}
	html, err := pickBody(sf.html, sf.htmlFile)
	if err != nil {
		return nil, fmt.Errorf("html body: %w", err)
	}
	if text == "" && html == "" {
		return nil, errors.New("at least one of --text/--text-file or --html/--html-file is required")
	}
	if text != "" {
		body["text"] = text
	}
	if html != "" {
		body["html"] = html
	}

	if len(sf.headers) > 0 {
		hdrs := map[string]string{}
		for _, raw := range sf.headers {
			name, value, ok := strings.Cut(raw, ":")
			if !ok {
				return nil, fmt.Errorf("header %q must be 'Name: value'", raw)
			}
			hdrs[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
		body["headers"] = hdrs
	}

	if len(sf.attach) > 0 {
		attachments := make([]map[string]any, 0, len(sf.attach))
		for _, path := range sf.attach {
			a, err := readAttachment(path)
			if err != nil {
				return nil, err
			}
			attachments = append(attachments, a)
		}
		body["attachments"] = attachments
	}
	return body, nil
}

// parseRecipients accepts "alice@example.com", "Alice <alice@example.com>",
// and the bare-angle-bracket "<alice@example.com>" forms. Server-side,
// SendRequest accepts either a string or a {name, email} object.
//
// We emit {name, email} when ParseAddress finds a display name; emit
// the parsed bare email when there's no name (NOT the raw input —
// "<alice@example.com>" with the brackets retained would fail the
// server's address validator). Only fall back to raw input when
// ParseAddress fails entirely so the server can return the better
// error message.
func parseRecipients(raw []string) []any {
	out := make([]any, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if addr, err := mail.ParseAddress(r); err == nil {
			if addr.Name != "" {
				out = append(out, map[string]any{"name": addr.Name, "email": addr.Address})
			} else {
				out = append(out, addr.Address)
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// pickBody returns either the literal flag value or the contents of
// the named file. "-" reads from stdin so callers can pipe.
func pickBody(literal, file string) (string, error) {
	if literal != "" && file != "" {
		return "", errors.New("pass at most one of --text/--text-file (or --html/--html-file)")
	}
	if literal != "" {
		return literal, nil
	}
	if file == "" {
		return "", nil
	}
	if file == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readAttachment reads a file and returns the JSON shape the server
// expects (filename, content as base64, contentType inferred from
// extension if possible). Mirrors the _Attachment pydantic model.
func readAttachment(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", path, err)
	}
	mtype := mime.TypeByExtension(filepath.Ext(path))
	if mtype == "" {
		mtype = "application/octet-stream"
	}
	return map[string]any{
		"filename":    filepath.Base(path),
		"content":     base64.StdEncoding.EncodeToString(data),
		"contentType": mtype,
	}, nil
}
