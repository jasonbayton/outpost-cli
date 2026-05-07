// Package config loads and saves the operator's CLI configuration —
// the server URL, the API token, and any per-host overrides. The file
// lives at $XDG_CONFIG_HOME/outpost/config.toml (default
// ~/.config/outpost/config.toml on macOS/Linux) so multiple Outpost
// servers can be addressed from the same machine via the `--server`
// flag without losing the default.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the on-disk structure. New fields go here additively —
// readers must tolerate missing keys so older binaries can read newer
// files (and vice versa).
type Config struct {
	// DefaultHost is the hostname-only key used when --server isn't
	// passed and no OUTPOST_SERVER env var is set. Maps into Hosts.
	DefaultHost string                  `toml:"default_host,omitempty"`
	Hosts       map[string]HostConfig   `toml:"hosts"`
}

// HostConfig is the per-server credential set. We keep tokens here
// rather than in a system keychain to start with — operators tend to
// run this on their own machine, and the file mode is 0600. We can
// add keychain support later behind a `secret = "keyring"` flag.
type HostConfig struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// Load reads the config file from disk. A missing file returns a
// zero-value Config and no error — first-run callers should treat
// "no config" as "not logged in" rather than a fatal error.
func Load() (*Config, string, error) {
	path, err := configPath()
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Hosts: map[string]HostConfig{}}, path, nil
	}
	if err != nil {
		return nil, path, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &Config{Hosts: map[string]HostConfig{}}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, path, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]HostConfig{}
	}
	return cfg, path, nil
}

// Save writes the config to disk with 0600 permissions so a stray
// `chmod -R` or world-readable home directory doesn't expose tokens.
func Save(cfg *Config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.tmp")
	if err != nil {
		return path, fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return path, fmt.Errorf("chmod temp: %w", err)
	}
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(cfg); err != nil {
		_ = tmp.Close()
		return path, fmt.Errorf("encode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return path, fmt.Errorf("close temp: %w", err)
	}
	// Atomic replace so a concurrent reader never sees a partial write.
	if err := os.Rename(tmpName, path); err != nil {
		return path, fmt.Errorf("rename: %w", err)
	}
	return path, nil
}

// configPath honours XDG_CONFIG_HOME and falls back to
// $HOME/.config/outpost/config.toml on macOS/Linux. Windows uses
// %APPDATA%\outpost\config.toml via os.UserConfigDir.
func configPath() (string, error) {
	if env := os.Getenv("OUTPOST_CONFIG"); env != "" {
		return env, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "outpost", "config.toml"), nil
}

// Resolve returns the host config to use for this invocation, taking
// (in priority order): explicit --server flag, $OUTPOST_SERVER env,
// stored DefaultHost. Returns an error if none resolve to a host
// known in the config — callers can use that to print a "run
// `outpost auth login`" hint.
//
// Accepts URL-form ("https://outpost.example.org") or bare-host
// ("outpost.example.org") inputs interchangeably and normalizes to
// the same key the login flow stored. Otherwise `--server https://x`
// would resolve as a different host than `--server x` even though
// `auth login` saved them under the same key.
func (c *Config) Resolve(explicitServer string) (string, HostConfig, error) {
	want := explicitServer
	if want == "" {
		want = os.Getenv("OUTPOST_SERVER")
	}
	if want == "" {
		want = c.DefaultHost
	}
	if want == "" {
		return "", HostConfig{}, errors.New("no server configured. run `outpost auth login` first, or pass --server")
	}
	want = normalizeHostKey(want)
	host, ok := c.Hosts[want]
	if !ok {
		return want, HostConfig{}, fmt.Errorf("no stored credentials for %q. run `outpost auth login --server %s`", want, want)
	}
	return want, host, nil
}

// normalizeHostKey reduces a server identifier to the form `auth login`
// stored it under: lower-cased hostname only, no scheme, no trailing
// slash. Port numbers are preserved (port matters for non-default
// deployments).
func normalizeHostKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return strings.ToLower(strings.TrimSuffix(value, "/"))
}
