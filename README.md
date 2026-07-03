# moshdrop

**Keep mosh. Just drop files.**

A transparent [mosh](https://mosh.org) wrapper for macOS: drag any file into your terminal window and it lands on the remote machine — your input stream receives a ready-to-use remote path. Everything else passes through byte-for-byte, with sub-millisecond overhead.

> 🎬 *demo GIF coming here: drag a screenshot thumbnail into a remote Claude Code session, watch it appear as `[Image]`*

## Why

mosh is a state-synchronizing protocol, not a byte pipe — every in-band file-transfer trick (zmodem/lrzsz, trzsz, iTerm2 ⌥-drag, kitty transfer) dies at the mosh boundary. This has been an [open issue since 2014](https://github.com/mobile-shell/mosh/issues/1184).

Meanwhile, if you run **Claude Code / Codex or any AI CLI on a remote box over mosh + tmux**, you constantly want to feed it local screenshots. moshdrop makes that a single drag.

Unlike [tssh](https://github.com/trzsz/trzsz-ssh) (which replaces mosh entirely), moshdrop keeps your battle-tested mosh exactly as it is: it's a local pty proxy that ferries files over a parallel ssh channel — reusing your existing `~/.ssh/config` and keys, **zero installation on the remote**.

## Install

```sh
brew install rrrrrrrrrricoxi/tap/moshdrop        # recommended
```

Or grab a binary from [Releases](../../releases) (macOS arm64/amd64), or `go install github.com/rrrrrrrrrricoxi/moshdrop@latest`.

> Downloaded binaries (not via brew) are unsigned; first run: right-click → Open, or `xattr -d com.apple.quarantine moshdrop`.

## Use

```sh
moshdrop myhost -- tmux new -A -s main    # exactly like mosh — all args pass through
alias mosh=moshdrop                        # or take over the mosh command entirely
```

Then **drag any file into the terminal window** (Finder files, multi-select, even the macOS screenshot floating thumbnail). One-ish second later the remote path appears at your cursor, e.g. `/home/you/.moshdrop/Screenshot\ 2026-07-03.png` — paste-semantics preserved, so TUI apps like Claude Code treat it as a real attachment.

```sh
moshdrop paste myhost     # upload the clipboard image (Cmd+Ctrl+Shift+4 style);
                          # the remote path is copied back to your clipboard — just Cmd+V
moshdrop doctor myhost    # end-to-end health check: connectivity, remote dir, real probe upload
```

## Config (optional)

`~/.moshdrop/config` — plain `key = value`:

```ini
ttl_days = 7          # auto-clean remote files older than N days (0 = keep forever)
intercept = on        # off = pure passthrough, drag-upload disabled
lang = en             # en | zh
remote_dir = .moshdrop

host.workbox.remote_dir = drops    # per-host overrides
host.workbox.intercept = off
```

## How it works

```
your terminal ──stdin──▶ moshdrop ──pty──▶ mosh-client ──UDP──▶ mosh-server
                           │ detects a paste that is exactly 1..N local file paths
                           │ (verified against the local disk — false positives ≈ 0)
                           └──ssh (same config/keys, warm connection)──▶ remote ~/.moshdrop/
```

- **Order-preserving transparency**: non-drag bytes are never delayed or reordered; fuzz-tested byte-for-byte.
- **Fail-open**: any failure passes your original paste through untouched + a system notification explaining why (`~/.moshdrop/events.log` keeps a JSONL trail).
- **Integrity**: uploads go to a temp name, are byte-count-verified, then atomically linked into place — a dying connection can never leave a truncated file masquerading as a real one.

## Also wraps plain ssh, et, …

moshdrop is a generic pty wrapper; mosh is just the default. Anything that talks to a terminal works:

```sh
MOSHDROP_CMD=ssh moshdrop myhost              # drag-drop for plain ssh
MOSHDROP_CMD=et  moshdrop myhost              # Eternal Terminal
```

## FAQ

**Is my typing inspected?** Only complete terminal *pastes* are examined, and only swallowed when every token is an absolute path to an existing local file. Typed input is never touched, nothing is uploaded that you didn't drag.

**Exit codes?** Same as mosh (mosh does not propagate remote exit codes — protocol limitation).

**Linux?** Remote side: anything with `sh`. Local side is macOS-only for now — PRs welcome.

## License

MIT
