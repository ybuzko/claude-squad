# Fork notes: Linear-driven dispatcher

Internal Loop fork of [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad)
(AGPL-3.0 — do not distribute outside Loop). Adds a read-only Linear poller that
auto-spawns an interactive Claude Code session per ticket, plus hook-driven
`blocked` / `idle` status so the TUI shows which sessions are waiting on a human.

With `linear.enabled=false` (the default) the fork behaves like upstream.

## Upstream extension points

Where the fork hooks in. Keep this table current when rebasing.

| Concern | Upstream location | Fork touch point |
|---|---|---|
| Instance struct + `Status` enum | `session/instance.go` — `Running/Ready/Loading/Paused` as ints | `Blocked`, `Done` appended (JSON-compatible); `IssueID`, `IssueUUID`, `IssueURL`, `ClaudeSessionID` fields |
| Persistence | `session/storage.go` `InstanceData` → `~/.claude-squad/state.json` `instances[]` | mirrors the new fields, incl. `archived`, `archived_at`, `in_view`; archived instances live in the same array. Every save goes through `home.saveInstances()` (active list + archive): saving `m.list` alone would drop the archive |
| Instance creation | `app/app.go` `KeyNew`: `session.NewInstance` → `list.AddInstance` (finalizer) → `SetStatus(Loading)` → finalizer → background `Start(true)` → `instanceStartedMsg` | `app/dispatch.go` reuses exactly this path; `instanceStartedMsg.autoSpawned` suppresses focus steal + help overlay |
| Tick loop | `app/app.go`: `previewTickMsg` (100 ms, redraw) and `tickUpdateMetadataCmd` → `metadataUpdateDoneMsg` (500 ms; `HasUpdated()` + git diff per instance in goroutines, then sets `Running`/`Ready` on the main thread) | status-file read rides the 500 ms tick; diff stats (`git add -N .` + `git diff`, ~0.1–0.15 s CPU each in the backend repo) are throttled per instance by `session/diff_schedule.go`: selected every 5 s, others only after activity (pane or hook status changed) at most every 5 s, else every 60 s — upstream ran them for every instance every 500 ms, ~0.6 core per idle session; the diff base is the merge base of HEAD and `origin/HEAD` (or the spawn base ref) when that is newer than the worktree's creation commit (`git.diffBase`), since agents rebase onto origin/main and diffing against the creation commit counted all of main's changes since; `metadataUpdateDoneMsg` prefers hook state over the pane-diff heuristic; a third chained cmd (`linearPollTickMsg`) polls Linear |
| Status heuristic | `session/tmux/tmux.go` `HasUpdated()` — sha256 of `capture-pane`; changed → `Running`, else `Ready` | unchanged; used only for instances without a status file |
| tmux session | `tmux new-session -d -s claudesquad_<title> -c <worktree> <program>` | `-e CS_STATUS_KEY=<key>` on every session (see the status contract), plus `-e CS_ISSUE_ID=<id>` for ticket instances; Claude commands get `--remote-control=<name>` (`Instance.withRemoteControl`: "TSA-196 <ticket title>" for tickets, the title otherwise), on first start and on resume, so every session shows up in claude.ai and the Claude app |
| Worktree + branch | `session/git/worktree.go`: branch = `BranchPrefix + title` (lowercased by `sanitizeBranchName`), path `~/.claude-squad/worktrees/<branch>_<hex>`, base = `HEAD` of repo | `NewGitWorktreeForBranch(...)` takes an explicit branch + base ref (`origin/main`, fetched first) |
| Pause / Resume / Kill | `Instance.Pause()` keeps branch + path; `Resume()` restarts tmux with the same `Program`; `Kill()` drops worktree **and** branch | the `c` (checkout = pause) key is removed: it deleted the worktree while leaving the program running in it; `r` resumes in the background (`resumeInBackground`), and every pause, resume, archive and restore is logged with what triggered it; `D` archives any session, ticket or not (`Stop` = `Pause` + kill tmux, also what `p` runs); permanent delete (`Kill`) only from the archive view, which is available outside Linear mode too; `Resume()` of a Claude Code session (ticket or not) uses `claude --resume <session>` (the id comes from the hooks) / `claude -c`, or the original program when Claude never stored a conversation for the worktree (`claude -c` would exit at once and end the tmux session) |
| Config | `config/config.go`, `~/.claude-squad/config.json`, `cs debug` | `linear`, `spawn`, `instance_limit` sections |
| List rendering | `ui/list.go` `List.items` (user-ordered via J/K), `InstanceRenderer.Render` glyph by status, header `" Instances "` | urgency sort (working, including resuming/pausing → blocked → idle → paused → done; working first so the sessions that flip state all the time stay together at the top instead of jumping around a long idle list; idle sessions most recently idle first, by `StatusChangedAt`, taken from the hook timestamp for ticket sessions), header counts; `home`/`end` jump to first/last; compact two-line rows (ticket rows: `TSA-123 · <Linear state> (due 10/5)` over the ticket title, refreshed every poll, states containing "blocked" in red; other rows keep the branch); a left click selects a session (`List.ItemAtRow`), scrolled window that follows the selection with `↑/↓ N more` hints (upstream overflowed the viewport); the mouse wheel scrolls whichever pane is under the pointer, and over the list it moves the window without changing the selection (`List.ScrollBy`) until another session is selected (upstream's wheel always scrolled the preview) |
| Trust prompt | `Instance.CheckAndHandleTrustPrompt()` exists upstream but is never called | called on a backoff (1–21 s) after auto-spawn; moves the selection to "Yes, I trust this folder" before confirming, since current Claude Code defaults to "No, exit" |
| Entry point | `main.go` cobra root; requires cwd to be a git repo | `--linear` flag; `cs hook` subcommand; cwd check relaxed when `spawn.repo_path` is set |

Rendering fixes over upstream: the frame is sized to exactly the terminal height
(upstream was always one line over, and bubbletea drops lines from the *top* of a frame
that is too tall), `home.View` clips the frame to the terminal as a last guard, the
preview and terminal panes cut captured lines to the pane width instead of letting
lipgloss wrap them (after a resize the pane still holds old-width output, and
`capture-pane -J` joins wrapped lines), and tmux sessions are resized only once the
terminal size has held still for 200 ms, at which point the screen is also cleared
and fully repainted (Windows Terminal/ConPTY reflows its own copy of the screen on
resize, and bubbletea only rewrites lines it believes changed). `ctrl+l` repaints by hand.

Preview scrolling: Claude Code draws on the alternate screen, so its tmux panes have
no history and upstream's scroll mode (capture `-S -` into a viewport) could move one
line. `TmuxSession.ForwardScroll` sends wheel events (SGR, when the program tracks the
mouse) or PageUp/PageDown (alternate screen without mouse) into the pane instead, and
the live preview shows the program's own scrolled view. Ordinary panes keep the history
viewport, which now closes when another session is selected.

Terminal modes: attaching (`o`) runs a tmux client in the real terminal, and cs's
detach closes that client's pty, so tmux never restores what it changed. Two visible
effects: a leftover scroll region (DECSTBM) from attaching at a smaller size squeezes
every later frame into those rows (ConPTY keeps the region across resizes; tmux as a
terminal resets it, so it does not reproduce there without setting one by hand), and
application cursor mode makes Windows Terminal send Home/End as `ESC O H`/`ESC O F`
(bubbletea v1.3 reads them as alt+O + H/F). `tmux.ResetTerminalModes` (scroll region,
origin mode, auto-wrap, insert mode, charset, cursor keys, keypad) is written at startup
and after every detach, and prefixes every frame, so each full repaint (resize, ctrl+l,
the forced repaint after a detach) repairs the terminal. `translateSS3` maps the Home/End
sequences anyway.

Attached session size: tmux 3.6 sends the terminal `CSI 18t`/`CSI 14t` size queries
whenever its PTY reports no pixel size, and adopts the `CSI 8;rows;cols t` reply over
the PTY size. Windows Terminal (through ConPTY) answers with a stale size (163x41 here,
whatever the window is), so attached sessions came up at that size until a couple of
resizes. Every PTY cs starts or resizes now carries a nominal pixel size
(`tmux.winsize`), so tmux never asks.

Attach uses a fresh tmux client: cs keeps one background client per session for
sizing the preview, and nothing reads its output while the overview shows, so tens of
KB of frames drawn for the preview size pile up in its PTY. Attaching used to copy that
backlog to the terminal and leave a stale picture until a resize or two forced a full
redraw. `Attach` now replaces it with a new client started at the terminal's size
(`freshClient`), and the client started on detach starts at the last preview size
instead of 80x24.

The attached client's output passes through `tmux.altScreenFilter`, which drops tmux's
alternate-screen switches (`\e[?1049h`/`l`). cs is already on the alternate screen, and
on Windows Terminal the console (ConPTY) answers a repeated switch by rebuilding the
alternate screen at the main screen's size. The main screen isn't resized while an app
is on the alternate one, so after moving the window to another monitor every attach
(and the overview after it) came back at the old size, wrapped and garbled until a
resize, even though the size WSL reported was right.

Opening a session does not block the TUI. Upstream ran the attach inside Update and
waited there for the detach, so polling, spawning and the comment watcher stopped while
a session was open, and Bubble Tea's own stdin reader kept running: it took one chunk of
input, which the session never got and the list received as a keypress after the detach
(a lone `c` from the terminal's reply to tmux's startup queries is the likely cause of a
session paused mid-work). `termio` now owns the terminal: it puts it in raw mode, reads
stdin on one goroutine and passes each chunk to Bubble Tea through a pipe
(`tea.WithInput`) or, while a session is open, to that session (`termio.Divert`). The
TUI's output goes through `termio.Output`, which drops writes while a session is open,
so the event loop keeps running without drawing over it; the detach forces a full
repaint. `TmuxSession.attached` makes the TUI leave the open client alone (`SendKeys`
returns `ErrAttached`, `SetDetachedSize` only records the size), and the poller and the
comment watcher skip a session that is open (`Instance.Attached`). The detach no longer
waits for the attached client's output copier, whose read on the closed PTY can take
seconds to return; the copier's writer is stopped instead.

Dead code upstream worth knowing: `instanceStartDoneMsg` / `runInstanceStartCmd`
in `app/app.go` are unused; `instanceStartedMsg` is the live path.

## Setup

```bash
go build -o ~/.local/bin/cs .          # Go 1.25.8; ~/.local/bin must be on PATH
export LINEAR_API_KEY=lin_api_...      # in your shell profile
cs debug --linear                      # prints the config and lists the view's issues
cs                                     # or `cs --linear` to force it on
```

Merge the `hooks` object from `hooks/claude-settings.snippet.json` into
`~/.claude/settings.json`. Since `cs hook` exits immediately when `CS_ISSUE_ID` is
unset, the hooks are inert in every Claude Code session the dispatcher did not start.

Verified live against the TSA "Mine" view: auto-spawn with the concurrency cap,
hook-driven idle/blocked, pause → `claude --resume` after the tmux server lost the
session, `t` spawn of a ticket outside the poller's reach, Done detection, delete.
Claude Code records trust for a worktree under its main repo, so once `spawn.repo_path`
is trusted, new worktrees open without the dialog. A fresh clone is untrusted: the first
batch of spawns all hit the dialog, which the auto-dismiss answers.

## Keys added or changed in Linear mode

| Key | Behaviour |
|---|---|
| `t` | Prompt for an issue id (any ticket, in the view or not) and spawn it like the poller would. A ticket that already has a session is never spawned twice: an archived session is restored, with `spawn.resume_prompt`, and an active one is selected. Sessions are matched by Linear UUID, so a ticket that moved teams (TSA-312 → BILL-601) finds its session under either id |
| `D` | On any session (any state but loading/resuming): **archive** it. Plain `n`/`N` sessions too; upstream kill is gone (delete for good from the archive) |
| `a` | Toggle the archive view (also outside Linear mode). There: `r` restores (no prompt), `D` deletes for good, `a`/`esc` goes back |
| `p` | Pause (upstream: push): `Instance.Stop()` in the background, i.e. uncommitted work committed to the branch, worktree removed, tmux and Claude ended; the session stays in the list. Asks first only if Claude is mid-turn. Paused tickets are still watched: new comments by others resume them with the update prompt |
| `P` | Commit and push the branch (upstream `p`) |
| `r` | Resume a paused session (e.g. after `p` or after its tmux server died), in the background |
| `c` | Removed (upstream checkout/pause) |
| `n` / `N` | Unchanged — plain sessions for non-ticket work, created from `spawn.repo_path` |

## Messages from outside (`cs send`)

`cs send <session title or ticket id> "<prompt>"` (or the prompt on stdin) queues a
prompt for a session without opening it; a script or another Claude session can use it
too. It writes `~/.claude-squad/outbox/<name>.md`, and the running cs delivers it on a
Linear poll the way ticket updates are delivered (`app/outbox.go`):

- **Paused session:** resumed, then the prompt is typed in once `SessionStart` reports.
- **Idle session:** typed in when the hook status accepts a prompt and the input box is
  empty. Busy sessions, and the one you have open, wait for a later poll.
- **Archived or unknown session:** the message waits in the outbox (logged once); it is
  delivered if the session is restored.
- A message starts work, so each poll delivers only while fewer than
  `linear.max_concurrent` sessions are working; the rest follow as those finish.
- The prompt is flattened to one line. A second `cs send` to the same session before
  delivery replaces the first. Delivered files move to `outbox/sent/<name>-<time>.md`.
- Linear mode only: the outbox rides the Linear poll.

## Archive

Sessions are never thrown away implicitly; they move to an archive and come back with
their full context. Everything below the first two bullets is about ticket sessions,
which the poller also archives and restores; plain `n`/`N` sessions are archived only
with `D`, restored only with `r` in the archive, and resume with `claude -c`.

- **Archived** = paused (uncommitted work committed to the branch as
  `[claudesquad] update from …`, worktree removed, branch kept) with tmux and Claude
  stopped, moved out of the active list. Archived sessions do not count against
  `instance_limit` or `max_concurrent` and are not polled by the metadata tick.
- **Restored** = worktree recreated at its **recorded path** from the branch, setup
  command run, `claude --resume <session id>`. Same path matters: Claude Code files
  transcripts by cwd, so that is how the resume finds the conversation.
- **Leaves the view → archived.** The poller compares each ticket's presence in the view
  with `in_view` from the previous poll and acts only on changes. A session that is
  running/loading when its ticket leaves is archived on a later poll, once idle or blocked.
- **Moved to another team** (new id, same Linear UUID): every poll matches tickets by
  UUID and updates the session's id, URL and row; the title, tmux session, branch and
  status file keep the old id.
- **Returns to the view → restored** under `max_concurrent`, queued with new spawns in
  view order. Once the `SessionStart` hook reports (status file reappears; 40 s timeout),
  `spawn.resume_prompt` is typed in.
- **Archived by hand (`D`) while still in the view** stays archived until the ticket
  leaves and comes back. Tickets started with `t` from outside the view are archived by
  the poller only after they have entered the view and left it.
- A session paused because its tmux died keeps its worktree on disk; archiving it
  commits any uncommitted work to the branch (locally) and removes that worktree too.
- If archiving fails (e.g. the commit fails), the session goes back to the active list
  and an automatic archive is retried on the next poll.
- Permanent delete (archive view, `D`) removes the record, branch and status file. The
  Claude transcript under `~/.claude/projects/` stays; transcripts are only kept forever
  if `cleanupPeriodDays` in `~/.claude/settings.json` is large (default 30 days).
- Archived branches exist only in `spawn.repo_path`; deleting branches there loses them.

## Ticket updates (`linear.watch_updates`)

New comments by other people on an active ticket are handed to its session, so an
answer to something the agent asked for gets acted on before the next human touch
(`app/updates.go`).

- Each poll also asks Linear for comments on the active (non-archived) ticket sessions
  created after their `linear_seen_at` (one query, capped at 14 days back). Comments by
  the API key's own user are skipped: they are the user's or an agent's posting as the
  user, and the two look the same.
- `linear_seen_at` is persisted per instance. It starts at spawn/restore time (triage
  and the resume prompt read every comment so far), and at the first poll for sessions
  that predate the feature, so turning it on does not replay history.
- **Idle session:** `linear.update_prompt` is typed in once the hook status accepts a
  prompt (`idle`, not a permission or question dialog) and Claude's input box is empty
  (`tmux.inputBoxEmpty`: nothing after `❯` except the dimmed suggestion). Busy sessions
  wait; anything that piles up goes in as one prompt. Nothing is delivered to a session
  while you have it open.
- **Paused session:** resumed, then the prompt is typed in once `SessionStart` reports
  (same path as archive restores). Archived sessions are not watched.
- The prompt lists authors and times only; the agent reads the comments from Linear
  itself and is told to treat them as information, not instructions or approval, and to
  stage and dry-run but not apply writes, post or merge.
- A row gets 💬 when an update went in (from this or the PR watcher); it clears when
  the session is opened.

## PR watcher (`pr_watch.enabled`)

Sessions are told about their pull requests (`app/prwatch.go`, `session/pr.go`,
`github/prs.go`). It works for every session in the list, ticket or not, and runs
whether or not Linear mode is on.

- **Finding PRs:** Claude Code writes a `{"type":"pr-link","prUrl":…}` entry into the
  transcript (`~/.claude/projects/<worktree path, non-alphanumerics → '-'>/*.jsonl`)
  whenever the conversation's branch has a PR. Every `pr_watch.poll_interval_sec`
  (default 300) the watcher reads what was appended to those files since the last poll;
  this works for paused sessions too, since the transcripts outlive the worktree.
- **Checking them:** one `gh api graphql` call (batches of 20) returns each PR's state and
  the checks on its head commit. PRs marked closed are no longer looked up.
- **CI failed:** a check run that ended `FAILURE`, `TIMED_OUT` or `STARTUP_FAILURE` (or a
  commit status `FAILURE`/`ERROR`) on the head commit sends `pr_watch.ci_failed_prompt`,
  once per commit, as soon as the first check fails (the prompt says how many still run).
  A new failing commit is told again.
- **Merged:** sends `pr_watch.merged_prompt`, once. PRs merged before the watcher first
  looked at the session (`pr_watch_since`, persisted) are marked closed without a prompt, so
  turning it on does not replay old merges. PRs closed without merging are dropped silently.
- Delivery is the same as for Linear comments: typed into an idle session with an empty
  input box, a paused session is resumed with it, and busy, loading or open sessions get
  it on a later poll. One prompt carries everything new for a session. The two watchers
  share the in-flight set, so they never type over each other. Rows get 💬.
- Archived sessions are not watched. Errors (no `gh`, GitHub down) go to the log and
  the next poll retries.

## Desktop notifications (`notify`)

A notification goes out when a session needs you (a `Notification` hook: permission
prompt, question) and, with `"notify": "blocked+idle"` (the default), when it finishes a
turn (a `Stop` hook), with the reason or TL;DR line (`app/notify.go`, `notify/`).
`"blocked"` limits it to the first, `"off"` turns it off.

- Driven by hook status files only, so sessions without one never notify.
- Nothing for the session you have open, or one cs is pausing, resuming or has archived.
  The first look after cs starts only records where things are.
- Events are gathered for 3 s and shown as one notification ("1 needs you, 2
  finished"). An event that is over by then (the session got more work from a watcher,
  or the prompt was answered from the Claude app) is dropped.
- WSL: a Windows toast through `powershell.exe` (WinRT API, shown as Windows
  PowerShell; about 1 s per toast, off the UI thread). macOS: `osascript`. Other
  Linux: `notify-send`. Clicking the toast does not open the session.

Every Claude session cs launches also gets `--append-system-prompt` with `recap_prompt`
(a final "TL;DR:" line each turn, for skimming previews); `"recap_prompt": "off"` turns
it off. It only reaches sessions launched or resumed after the change.

## Status contract (Claude Code hooks → TUI)

`cs hook` is installed as a Claude Code hook (see `hooks/claude-settings.snippet.json`).
It is a no-op unless `CS_STATUS_KEY` (or, for ticket sessions started before it
existed, `CS_ISSUE_ID`) is set in the environment, which only cs does, so other
Claude Code sessions are unaffected.

Every session cs starts or resumes gets `CS_STATUS_KEY` (`Instance.StatusKey`): the
ticket id it was spawned for (its title) for ticket sessions, `session-<title>` for
`n`/`N` sessions. The key never changes, even when a ticket moves teams. Sessions that
were already running when this landed (plain ones) keep the pane heuristic until they
are paused and resumed. With the hook, plain sessions get blocked/idle too, `claude
--resume <id>` on resume instead of `claude -c`, and an accurate "accepts a prompt"
check for the PR watcher.

File: `~/.claude-squad/status/<key>.json`

```json
{"state":"blocked|idle|running","ts":1730000000,"reason":"…","session_id":"…","event":"Stop"}
```

`event` is the hook event that wrote the state; the notifier uses it to tell a finished
turn (`Stop`) from a resume (`SessionStart`).

| Hook event | Writes |
|---|---|
| `Notification` (matcher `permission_prompt\|elicitation_dialog\|elicitation_url_dialog\|agent_needs_input`) | `blocked`, reason = message |
| `Stop` | `idle`, reason = last line of the assistant message |
| `UserPromptSubmit`, `PostToolUse` | `running` |
| `SessionStart` | `session_id`; `running` on `startup`, `idle` on `resume`/`clear`, no state change on `compact` |

`idle_prompt` notifications are excluded by the matcher so they never overwrite `idle` with `blocked`.

## Config

```json
{
  "linear": {
    "enabled": true,
    "api_key": "",
    "view_id": "a3dd49a15e58",
    "poll_interval_sec": 15,
    "max_concurrent": 5,
    "done_state_types": ["completed", "canceled"],
    "done_state_ids": [],
    "watch_updates": true,
    "update_prompt": "New comments on {ISSUE_ID} in Linear since you last checked: {UPDATES}. …"
  },
  "spawn": {
    "program": "claude \"/triage-linear-ticket {ISSUE_ID}\"",
    "repo_path": "/home/yaroslav/.claude-squad/repos/backend",
    "branch_prefix": "agent/",
    "setup_command": "pnpm install --frozen-lockfile",
    "resume_prompt": "{ISSUE_ID} is back in your queue (reopened or requested again). …"
  },
  "pr_watch": {
    "enabled": true,
    "poll_interval_sec": 300,
    "ci_failed_prompt": "CI is failing on your PR {PR_URL} (commit {SHA}): {CHECKS}. …",
    "merged_prompt": "Your PR {PR_URL} was merged. If the change only takes effect once deployed, …"
  },
  "instance_limit": 50,
  "notify": "blocked+idle",
  "recap_prompt": "… End every turn with a final line that starts with \"TL;DR:\" …"
}
```

- `LINEAR_API_KEY` in the environment overrides `linear.api_key`.
- `spawn.repo_path` should be a clone dedicated to `cs`, not a checkout anyone works
  in: every spawn runs `git fetch` there and creates branches and worktrees from it.
  `cs reset` only clears `~/.claude-squad/worktrees`, so a clone under
  `~/.claude-squad/repos/` survives it.
- `view_id` is the view's UUID or the slug id that ends its URL
  (`linear.app/<ws>/team/<team>/view/<name>-<slugId>` → `<slugId>`).
- The view is the only filter: every issue it returns gets a session. Exclude
  finished, assigned-elsewhere, or otherwise unwanted tickets in the view itself.
- `max_concurrent` caps instances that are actually working (`Running`/`Loading`);
  `blocked`, `idle`, `Paused`, `Done` do not count. `instance_limit` caps active
  (non-archived) instances for the dispatcher only: it spawns and restores until the count
  reaches it. Manual `n`, `N`, `t` and archive `r` always work and may go past it.
- Done detection: an instance is `Done` when its issue's workflow state has a type in
  `done_state_types`, or an id in `done_state_ids`. A reopened ticket flips back. This is
  only a label (✓); archiving is driven by view membership.
- `spawn.resume_prompt` is typed into a restored ticket session; `{ISSUE_ID}` is
  substituted. The default asks Claude to re-read the ticket and re-check against current
  `origin/main` and data before continuing.
- Branch: `agent/<issue-id-lowercase>-<slugified-title>`, from `origin/main`
  (fetched right before the worktree is created).
- `spawn.setup_command` runs with `$SHELL -lc` inside every new worktree (ticket or
  `n`/`N`) before the program starts, and again when `r` recreates a paused
  worktree. The instance shows "Setting up workspace…" meanwhile; a non-zero exit
  fails the start and names the log at `~/.claude-squad/setup-logs/<title>.log`.
  A worktree has no `node_modules`/`dist`, so without this the agent can read and
  edit but not run anything.
- Worktree path stays upstream's `~/.claude-squad/worktrees/<branch>_<hex>`; the
  path is recorded on the instance and reused by Pause/Resume, which is what keeps
  `claude --resume` pointed at the right transcript.
