# `outpost` CLI

Remote-administration command-line tool for an [Outpost](../README.md) mail
server. Talks to the server's `/admin/api` and `/mail/send` HTTPS surfaces;
authenticates with an API key minted on the server.

For on-server work (when you're SSH'd in), use `mailctl` instead — that's the
direct-DB tool that keeps working during recovery. `outpost` is the laptop
and CI tool.

## Install

### One-line installer (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/jasonbayton/outpost-cli/main/install.sh | sh
```

Detects your OS + arch, downloads the matching tarball from the latest
release, verifies the SHA-256 against the release's `checksums.txt`, and
installs the binary into the first writable directory it finds in:
`$OUTPOST_INSTALL_DIR`, `/usr/local/bin`, `$HOME/.local/bin`, `$HOME/bin`.

Pin a version with `OUTPOST_VERSION=v0.1.0 curl ... | sh`.

### Go install

```sh
go install github.com/jasonbayton/outpost-cli/cmd/outpost@latest
```

### Binary downloads

Each release at <https://github.com/jasonbayton/outpost-cli/releases> ships
static binaries for darwin-arm64, darwin-amd64, linux-arm64, linux-amd64,
and windows-amd64. Download the tarball / zip and put the binary on your
`$PATH`.

### Homebrew (planned)

`brew install jasonbayton/tap/outpost` will work once the homebrew tap
repo is set up. Until then, use the one-line installer or `go install`.

## First run

1. SSH to the server and mint an API key for your account:

   ```sh
   sudo mailctl key create your-account@example.org --label "laptop-cli" --scope jmap
   ```

   The command prints the token once. Copy it.

2. On your laptop:

   ```sh
   outpost auth login --server https://outpost.example.org --set-default
   # paste the token when prompted
   ```

The token lands in `~/.config/outpost/config.toml` (mode `0600`).

## Usage

```sh
outpost health
outpost domain list
outpost domain dns template greenrobot.co > greenrobot.zone
outpost reconcile status
outpost mail send -f postmaster@example.org -t me@example.com -s probe --text hi
```

Add `--json` to any command for machine-readable output (handy in CI).

## Multiple servers

```sh
outpost auth login --server https://outpost.example.org
outpost auth login --server https://staging.example.org

outpost --server staging.example.org domain list
```

The default server (used when `--server` isn't passed) is whichever you logged
in to first, or whichever you most recently set with `--set-default`.

`$OUTPOST_SERVER` is the env-var override if you don't want a flag.

## Building from source

```sh
cd cli
go build -o outpost ./cmd/outpost
./outpost --help
```
