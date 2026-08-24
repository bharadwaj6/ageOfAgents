# Install

**Prerequisites:** Go **1.26.4+** (see [`go.mod`](https://github.com/bharadwaj6/ageOfAgents/blob/main/go.mod))
and `git`. Nothing else — the default backend runs offline with no API key.

## go install

```bash
go install github.com/bharadwaj6/ageOfAgents/cmd/aoa@latest
```

Puts `aoa` in `$GOBIN` (or `$GOPATH/bin`). Make sure that's on your `$PATH`.

## Release binary

Grab a `tar.gz` (or `.zip` on Windows) for your platform from
[Releases](https://github.com/bharadwaj6/ageOfAgents/releases), unpack it, and put `aoa` on your `$PATH`.
Builds are published for Linux, macOS and Windows on amd64 and arm64.

Verify it:

```bash
aoa version
```

## From source

```bash
git clone https://github.com/bharadwaj6/ageOfAgents.git && cd ageOfAgents
make install          # -> $GOBIN/aoa
# or, without touching your PATH:
make build            # -> ./aoa
```

`make help` lists every target.

## Shell completions

```bash
aoa completion zsh  > "${fpath[1]}/_aoa"   # then: compinit
aoa completion bash > /etc/bash_completion.d/aoa
aoa completion fish > ~/.config/fish/completions/aoa.fish
```

Or source it directly — `source <(aoa completion bash)`.

## Check your machine

```bash
aoa doctor --path ./workspace
```

`doctor` verifies git, the workspace, your `aoa.toml`, the configured backend's CLI, every Gate command,
docker when you use it, and that the Event Log replays. Each failure prints the one action that fixes
it, and it exits non-zero so CI can gate on it.

## Next

- [Get started](getting-started.md) — a first run, offline, in about ten seconds
- [Pick a harness](harnesses/README.md) — Claude Code, Codex, Cursor, Grok, Gemini, or your own CLI
