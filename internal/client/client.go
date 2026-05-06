// Package client wraps the HTTP layer used by every command. Single
// place for: bearer-token injection, JSON encode/decode, error
// surfacing (so a 422 with {"detail": ...} prints the detail rather
// than a raw status code), timeouts, and User-Agent stamping.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jasonbayton/outpost-cli/internal/config"
)

// DefaultTimeout caps any single request. Mail sends and DNS verifies
// are interactive so a >30s call almost always means something is
// hung; better to fail fast than let the operator wonder.
const DefaultTimeout = 30 * time.Second

// Client is bound to one server + one token. Construct via New().
type Client struct {
	base       *url.URL
	token      string
	httpClient *http.Client
	userAgent  string
}

// New constructs a Client from a HostConfig. Returns an error if the
// stored URL doesn't parse — that's recoverable by re-running login,
// not by retrying the same call.
func New(host config.HostConfig, version string) (*Client, error) {
	if host.URL == "" {
		return nil, errors.New("host config has no URL")
	}
	parsed, err := url.Parse(strings.TrimRight(host.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse server url: %w", err)
	}
	return &Client{
		base:       parsed,
		token:      host.Token,
		httpClient: &http.Client{Timeout: DefaultTimeout},
		userAgent:  fmt.Sprintf("outpost-cli/%s", version),
	}, nil
}

// APIError is the structured error returned when the server responds
// with a non-2xx status. The Detail field carries the server's own
// "detail" string so commands can print actionable messages.
type APIError struct {
	Status int
	Code   string
	Detail string
	Body   []byte
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, http.StatusText(e.Status))
}

// IsUnauthorized is the typed check commands use to suggest
// `outpost auth login` when a 401 comes back from a stale token.
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

// Do sends a request and decodes the response body into out.
//
// Both `body` and `out` may be nil. If the response is non-2xx, the
// returned error is an *APIError carrying the parsed `detail` field
// when the body is JSON-shaped, falling back to the raw body
// otherwise.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		enc := json.NewEncoder(buf)
		// We don't need HTML-escaped JSON over the wire (and "<" in a
		// subject line shouldn't become "<" silently).
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		bodyReader = buf
	}

	full, err := c.base.Parse(path)
	if err != nil {
		return fmt.Errorf("build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, full.String(), bodyReader)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Body: respBody}
		// Try to surface the structured fields the FastAPI handlers
		// emit. Both `detail` (FastAPI's HTTPException default) and
		// `code` (our custom shape on /mail/send) are pulled out.
		var parsed struct {
			Detail any    `json:"detail"`
			Code   string `json:"code"`
		}
		if json.Unmarshal(respBody, &parsed) == nil {
			switch d := parsed.Detail.(type) {
			case string:
				apiErr.Detail = d
			case map[string]any:
				if b, err := json.Marshal(d); err == nil {
					apiErr.Detail = string(b)
				}
			}
			apiErr.Code = parsed.Code
		}
		return apiErr
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// DoRaw is the escape hatch for endpoints that don't return JSON
// (e.g. `/admin/api/domains/{id}/dns/zonefile` returns text/plain).
// Returns the response body so callers can write it to stdout / a
// file. Honours the same 2xx / APIError contract as Do.
func (c *Client) DoRaw(ctx context.Context, method, path string) ([]byte, string, error) {
	full, err := c.base.Parse(path)
	if err != nil {
		return nil, "", fmt.Errorf("build url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, full.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode, Body: body}
		var parsed struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			apiErr.Detail = parsed.Detail
		}
		return nil, "", apiErr
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// Base returns the underlying server URL — handy for printing
// "talking to https://outpost.bayton.org" prompts.
func (c *Client) Base() string { return c.base.String() }
