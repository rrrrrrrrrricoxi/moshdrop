# Late-upload delivery

Introduced in v1.2.0. The production helper's
user-confirmed dummy-data UI checks passed: independent A/B copying, grouped paths,
copying after the sender/posting helpers exited, dismissal, and ordinary strip
click without copying. Distribution and upgrade acceptance remain separate.

## User behavior

- The default automatic-insertion window is **5 seconds from accepting a drop or
  Ctrl+V image action**. It includes clipboard probing, queue time, transfer time,
  and waiting for clean terminal input. The decision is checked again immediately
  before the PTY write, under the scanner/write lock.
- Before that cutoff, successful uploads insert as before. Quick drag failures
  replay the original local paste; clipboard-image failures never replay input.
- At or after the cutoff, successful uploads **do not insert or copy anything**.
  They save a grouped result locally and request an actionable notification.
- Choose **Copy path** to copy that notification's saved result, then use the
  terminal's normal Paste action in the intended pane. The notification body and
  dismissal do not copy. macOS may hide the action until you hover over or expand
  the notification. Terminal paste is commonly Cmd+V on macOS; the existing lone
  Ctrl+V image shortcut is unchanged and is not a new pending-delivery shortcut.
- Late failures do not replay a local path into the currently active pane. They
  retain failure metadata and notify; no copy action is available for a failed
  upload. A failed multi-file transfer may have uploaded some files remotely, but
  it is not presented as a successful complete group.
- Clipboard copying happens only after an explicit Copy path click or local
  `moshdrop pending --copy <id>` command. A late image upload does not automatically
  replace the source image in the clipboard.
- This is a time-based mitigation, **not pane tracking**. Switching panes during
  a quick upload can still receive an automatic insertion in the new pane. Set
  `auto_insert_seconds = 0` to require explicit copying for all intercepted uploads.

## Local recovery

Run these on the **Mac that is running moshdrop**, not on the remote machine:

```sh
moshdrop pending                 # recent held successes and failures, newest first
moshdrop pending workbox         # exact upload-target/host filter
moshdrop pending --copy <id>     # copy the entire selected successful group
```

Results remain available after dismissal and after the originating session exits,
provided the upload completed and the result was saved before process exit.
Copying does not remove a result. Copy failures exit nonzero and do not claim
success. `moshdrop paste <host>` likewise prints the uploaded path but returns
nonzero and reports an error if clipboard copying fails.

The recovery command requires neither the helper nor notification permission.
It copies the same escaped, space-separated paths that terminal injection uses.
It does not re-upload files or verify that remote files still exist; remote TTL
cleanup is independent.

## State and limits

`~/.moshdrop/pending/` (or `MOSHDROP_STATE_DIR/pending/`) contains one JSON record
per held result: opaque random ID, host, source, filenames, remote paths, grouped
clipboard text, completion timestamp, and success/error. **No file/image contents,
SSH credentials, or clipboard image data are stored in these records.** Hostnames,
filenames, paths, and error text are still private metadata.

- Directory: owned, mode 0700, not a symlink.
- Records: owned regular single-link files, mode 0600; symlinks/hardlinks rejected.
- Atomic temporary-file write and rename; concurrent local sessions use unique IDs.
- Keep at most the newest 100 valid records, with a 7-day local retention period.
  Pruning occurs on saves and listing, not via a daemon. Direct copying also rejects
  expired records. Each record is limited to 8 MiB.
- Unrelated files are not deleted. Unsafe/corrupt matching records produce an error
  rather than silently deleting potential user data. Cleanup/storage failure can
  prevent normal history updates; the notification reports it, and never falls
  back to late input injection or automatic clipboard copying.
- Metadata checks protect the expected local state format; they are not an OS
  sandbox against another process running as the same user.

## Optional macOS notification helper

`MoshdropNotifier.app` is a small on-demand AppKit/UserNotifications helper, not a
background service, launch agent, or login item. It posts and exits. macOS can
relaunch it for a notification action. The action reads the exact saved upload ID
and does not rely on a process-local "most recent upload" variable.

Only **explicit setup** requests alert permission:

```sh
moshdrop notifications setup
```

Ordinary uploads do not request permission. If the helper is absent, unavailable,
notifications are denied/disabled, or posting fails, the original simple
notification gives the local recovery command. If all notifications are disabled,
`moshdrop pending` remains usable. Focus settings can suppress a successfully
scheduled notification; API success is not proof that the user saw a banner.

The helper has the bundle ID
`io.github.rrrrrrrrrricoxi.moshdrop.notifier`, bound to its Info.plist by whole-app
ad-hoc signing. It targets macOS 13+ and includes arm64 and x86_64 code. Older
systems can use the Go recovery command without this helper.

**Ad-hoc signing is not Developer ID signing or notarization.** Distribution may
encounter Gatekeeper/quarantine, and alert permission continuity across changed
builds or Homebrew upgrades is not guaranteed. No install hook automatically
removes quarantine, modifies notification settings, or requests permission.
Before release, manually verify trusted distribution and upgrade behavior; do not
weaken macOS signing requirements merely to preserve permission.

## Build and packaging

Source builds need macOS Command Line Tools with Swift. No new Go dependencies or
third-party helper runtime are introduced. For an isolated local artifact layout:

```sh
mkdir -p dist/local
go build -o dist/local/moshdrop .
bash native-notifier/build.sh dist/local
# This is a pure check: no native UI, clipboard access, or permission request.
dist/local/MoshdropNotifier.app/Contents/MacOS/MoshdropNotifier --selftest
```

The build script signs the **entire app**, validates the plist/signature, and
refuses to overwrite an unrecognized bundle. Generated app bundles and `dist/`
are ignored by git. It does not install or launch the app.

GoReleaser's before hook builds the universal helper at the repository root, and
release archives include the app alongside `moshdrop`. The existing Homebrew
formula flow installs the executable in `bin` and the helper in `libexec`.
The executable resolves its own symlink before locating the helper, so normal
Homebrew bin symlinks lead to the corresponding keg. Plain extracted archives
keep the app beside the executable. `go install` alone has no app: recovery still
works. `MOSHDROP_NOTIFIER_APP` can explicitly select a development helper bundle.

## Verification and acceptance

Automated checks run with fake upload/clipboard/notification boundaries, private
temporary state, and real SSH E2E disabled. Coverage includes quick/late grouped
success, transfer/queue/input-wait cutoff crossing, no late replay, image cleanup,
no automatic clipboard copying, missing-helper fallback, storage failure, local
copy failure, host filtering, persistence, expiry/count pruning, concurrent saves,
unsafe-file rejection, and Go/Swift record-format agreement.

CI builds/signs both helper architectures and runs its pure self-checks before
Go tests. `MOSHDROP_TEST_NOTIFIER` enables pure cross-language fixture checks
without loading notification center or the system clipboard.

User-approved manual UI checks passed on the built production helper with dummy
records: alert setup (`granted=true`), independent A and grouped B Copy path actions,
copying after the dummy sender/posting helpers exited, dismissal without copying,
and ordinary notification-strip click without copying. Strip clicks can close the
notification; disappearance alone is not a failed check. Clipboard outcomes were
reported by the user, not inspected by the agent.

This is controlled helper UI acceptance with dummy records, not a real
SSH/upload-session test. CI also validates snapshot archive layout, helper
signatures/architectures, and generated Homebrew formula syntax without publishing.
GoReleaser is pinned to v2.18.3 in CI and release workflows.

Before installation/release, obtain separate approval for remaining checks:

1. Disable notification permission and verify the local recovery command.
2. Replace/update/move the helper and check action relaunch and permission continuity.
3. Confirm the GoReleaser configuration and actual archive/Homebrew installation.

No live SSH, system clipboard operations, production permission prompts,
installation, commit, tag, or release are part of automated verification.
