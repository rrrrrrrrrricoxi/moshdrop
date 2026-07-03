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

moshdrop sits between your terminal and mosh, like a doorman:

```
you ──▶ moshdrop ──▶ mosh ──▶ remote
```

- **Almost all the time it does nothing.** Every keystroke passes straight through, untouched, in order, with no measurable delay.
- **When you drag a file in**, your terminal actually just "types" the file's local path. moshdrop recognizes that, double-checks the file really exists on your disk, quietly uploads it over a second ssh connection (the same config and keys mosh already uses — nothing to install on the remote), and types the *remote* path instead. To the app on the other side it looks like you pasted a path that simply works.
- **If anything fails** — no network, remote disk full — your original paste appears exactly as-is, plus a notification telling you why in plain words. It never blocks or eats your typing. (`~/.moshdrop/events.log` keeps a one-line record per drop.)
- **Files can't arrive broken.** Uploads go to a hidden temp name, the byte count is verified, and only then does the file get its real name. A connection that dies mid-transfer leaves nothing behind.

## Resource footprint (measured, Apple Silicon)

| | |
|---|---|
| CPU when idle | **0.0%** — fully event-driven, no polling |
| CPU while typing at ~200× human speed | ~0.4% |
| Memory | ~5 MB |
| Added keystroke latency vs bare mosh | ~0.1 ms (within measurement noise) |
| Network when idle | **zero** — the ssh side channel closes itself 2 min after the last transfer |
| Network during transfers | file size + a few % ssh overhead, over one reused connection |
| Keepalive | none of its own when idle; an active transfer channel pings every 15 s so a dead link is detected within ~1 min instead of hanging |

(mosh's own tiny UDP heartbeats are unchanged — moshdrop adds nothing to them.)

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
