# moshdrop

**Keep mosh. Just drop files.**

A transparent [mosh](https://mosh.org) wrapper for macOS: drag any file into your terminal window and it lands on the remote machine — your input stream receives a ready-to-use remote path. Everything else passes through byte-for-byte.

> 🎬 *demo GIF coming here: drag a screenshot thumbnail into a remote Claude Code session, watch it appear as `[Image]`*

## Why

mosh doesn't carry your bytes end-to-end — it syncs *screen state*. That's what makes it survive bad Wi-Fi, and it's also why every trick that smuggles files through the terminal (zmodem/lrzsz, trzsz, iTerm2 ⌥-drag, kitty transfer) dies at the mosh boundary. [Open issue since 2014](https://github.com/mobile-shell/mosh/issues/1184).

Meanwhile, if you run **Claude Code / Codex or any AI CLI on a remote box over mosh + tmux**, you constantly want to feed it local screenshots. moshdrop makes that a single drag.

Unlike [tssh](https://github.com/trzsz/trzsz-ssh) (which replaces mosh entirely), moshdrop keeps your battle-tested mosh exactly as it is: it's a local pty proxy that ferries files over a parallel ssh channel, reusing your existing `~/.ssh/config` and keys. Nothing to install on the remote.

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

Then **drag any file into the terminal window** (Finder files, multi-select, even the macOS screenshot floating thumbnail). About a second later — a few seconds if the side channel has to reconnect first — the remote path appears at your cursor as a paste, so TUI apps like Claude Code treat it like a real attachment.

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

- **Almost all the time it does nothing.** Keystrokes pass straight through, untouched and in order. (One deliberate exception: a *lone* ESC press is held ~50 ms to tell it apart from a paste marker — the same disambiguation trick as vim's `ttimeoutlen`. Complete escape sequences like arrow keys are not delayed.)
- **When you drag a file in**, your terminal actually just "types" the file's local path as a paste. moshdrop recognizes path-shaped pastes, double-checks every path is a real file on your local disk, quietly uploads them over a second ssh connection, and pastes the *remote* path instead.
- **If an upload fails** — no network, remote disk full — your original paste comes through unchanged once the attempt gives up (seconds for connection errors; up to the size-based timeout for a big file on a dying link), plus a notification saying why in plain words. Your typing is never blocked either way. `~/.moshdrop/events.log` keeps a one-line record per drop.
- **Files don't land half-written.** Uploads go to a hidden temp name, the byte count is checked (transport integrity is ssh's job), and only then is the file atomically linked to its real name. A connection dying mid-transfer leaves at worst a hidden temp file, swept on the next connect.

## Resource footprint (measured, Apple Silicon)

| | |
|---|---|
| CPU when idle | **0.0%** — fully event-driven, no polling |
| CPU at 2 KB/s sustained input (~200× human typing) | ~0.4% |
| Memory | ~5 MB |
| Added keystroke latency vs bare mosh | ~0.1 ms (within measurement noise; lone-ESC exception above) |
| Network when idle | **zero** — the ssh side channel closes itself 2 min after last use |
| Network during transfers | file size + a few % ssh overhead, over one reused connection |
| Keepalive | none of moshdrop's own once the side channel closes; while the channel is open (transfers + the 2-min linger) it pings every 15 s so a dead link is detected within ~1 min instead of hanging |

mosh's own tiny UDP heartbeats are unchanged — moshdrop adds nothing to them.

## Also wraps plain ssh, et, …

moshdrop is a generic pty wrapper; mosh is just the default. Anything that talks to a terminal works:

```sh
MOSHDROP_CMD=ssh moshdrop myhost              # drag-drop for plain ssh
MOSHDROP_CMD=et  moshdrop myhost              # Eternal Terminal
```

## FAQ

**Is my typing inspected?** Byte-at-a-time typing is never touched. Input is only examined when it arrives as a paste-like burst, and only swallowed when *every* token is an absolute path to an existing local regular file — which in practice means drags. Note the flip side: manually pasting text that happens to be exactly such a path is treated identically (the file uploads and the remote path is pasted). If that's ever not what you want, quote the path or set `intercept = off`.

**How does it detect a drag?** Terminals deliver a drop as a (usually [bracketed](https://en.wikipedia.org/wiki/Bracketed-paste)) paste of the file's path. When the remote app hasn't enabled bracketed paste, a stricter fallback heuristic catches typical drops; if nothing matches, the path text simply passes through — fail-open, always.

**What does the upload channel need?** It's non-interactive (`BatchMode`): if `ssh yourhost true` works without typing anything (keys/agent), you're set. Password or interactive-2FA auth will make drops fail (mosh itself still works). `moshdrop doctor yourhost` checks this end-to-end.

**Exit codes?** Same as mosh for normal exits (mosh itself never propagates remote command exit codes — protocol limitation). If mosh dies from a signal, moshdrop exits 1.

**Linux?** Remote side: any POSIX-ish `sh` + coreutils (`find -delete` needed for auto-cleanup; without it files just accumulate). Local side is macOS-only for now — PRs welcome.

## License

MIT
