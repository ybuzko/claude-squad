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
| Tick loop | `app/app.go`: `previewTickMsg` (100 ms, redraw) and `tickUpdateMetadataCmd` → `metadataUpdateDoneMsg` (500 ms; `HasUpdated()` + git diff per instance in goroutines, then sets `Running`/`Ready` on the main thread) | status-file read rides the 500 ms tick; `metadataUpdateDoneMsg` prefers hook state over the pane-diff heuristic; a third chained cmd (`linearPollTickMsg`) polls Linear |
| Status heuristic | `session/tmux/tmux.go` `HasUpdated()` — sha256 of `capture-pane`; changed → `Running`, else `Ready` | unchanged; used only for instances without a status file |
| tmux session | `tmux new-session -d -s claudesquad_<title> -c <worktree> <program>` | `-e CS_ISSUE_ID=<id>` added for ticket instances |
| Worktree + branch | `session/git/worktree.go`: branch = `BranchPrefix + title` (lowercased by `sanitizeBranchName`), path `~/.claude-squad/worktrees/<branch>_<hex>`, base = `HEAD` of repo | `NewGitWorktreeForBranch(...)` takes an explicit branch + base ref (`origin/main`, fetched first) |
| Pause / Resume / Kill | `Instance.Pause()` keeps branch + path; `Resume()` restarts tmux with the same `Program`; `Kill()` drops worktree **and** branch | ticket instances: `D` archives (`StopForArchive` = `Pause` + kill tmux); permanent delete (`Kill`) only from the archive view; `Resume()` uses `claude --resume <session>` / `claude -c` |
| Config | `config/config.go`, `~/.claude-squad/config.json`, `cs debug` | `linear`, `spawn`, `instance_limit` sections |
| List rendering | `ui/list.go` `List.items` (user-ordered via J/K), `InstanceRenderer.Render` glyph by status, header `" Instances "` | urgency sort (blocked → idle → running → paused → done), header counts; compact two-line rows (ticket rows: `TSA-123 (due 10/5)` over the ticket title, refreshed every poll; other rows keep the branch), scrolled window that follows the selection with `↑/↓ N more` hints (upstream overflowed the viewport) |
| Trust prompt | `Instance.CheckAndHandleTrustPrompt()` exists upstream but is never called | called on a backoff (1–21 s) after auto-spawn; moves the selection to "Yes, I trust this folder" before confirming, since current Claude Code defaults to "No, exit" |
| Entry point | `main.go` cobra root; requires cwd to be a git repo | `--linear` flag; `cs hook` subcommand; cwd check relaxed when `spawn.repo_path` is set |

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
| `t` | Prompt for an issue id (any ticket, in the view or not) and spawn it like the poller would. An archived ticket is restored instead, with `spawn.resume_prompt` |
| `D` | On a ticket session (any state): **archive** it. Plain `n`/`N` sessions keep upstream kill |
| `a` | Toggle the archive view. There: `r` restores (no prompt), `D` deletes for good, `a`/`esc` goes back |
| `r` | Resume a paused session (e.g. after its tmux server died) |
| `n` / `N` | Unchanged — plain sessions for non-ticket work, created from `spawn.repo_path` |

## Archive

Ticket sessions are never thrown away implicitly; they move to an archive and come back
with their full context.

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
- **Returns to the view → restored** under `max_concurrent`, queued with new spawns in
  view order. Once the `SessionStart` hook reports (status file reappears; 40 s timeout),
  `spawn.resume_prompt` is typed in.
- **Archived by hand (`D`) while still in the view** stays archived until the ticket
  leaves and comes back. Tickets started with `t` from outside the view are archived by
  the poller only after they have entered the view and left it.
- If archiving fails (e.g. the commit fails), the session goes back to the active list
  and an automatic archive is retried on the next poll.
- Permanent delete (archive view, `D`) removes the record, branch and status file. The
  Claude transcript under `~/.claude/projects/` stays; transcripts are only kept forever
  if `cleanupPeriodDays` in `~/.claude/settings.json` is large (default 30 days).
- Archived branches exist only in `spawn.repo_path`; deleting branches there loses them.

## Status contract (Claude Code hooks → TUI)

`cs hook` is installed as a Claude Code hook (see `hooks/claude-settings.snippet.json`).
It is a no-op unless `CS_ISSUE_ID` is set in the environment, which only the
spawner does, so normal Claude Code sessions are unaffected.

File: `~/.claude-squad/status/<ISSUE-ID>.json`

```json
{"state":"blocked|idle|running","ts":1730000000,"reason":"…","session_id":"…"}
```

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
    "poll_interval_sec": 60,
    "max_concurrent": 5,
    "done_state_types": ["completed", "canceled"],
    "done_state_ids": []
  },
  "spawn": {
    "program": "claude \"/triage-linear-ticket {ISSUE_ID}\"",
    "repo_path": "/home/yaroslav/.claude-squad/repos/backend",
    "branch_prefix": "agent/",
    "setup_command": "pnpm install --frozen-lockfile",
    "resume_prompt": "{ISSUE_ID} is back in your queue (reopened or requested again). …"
  },
  "instance_limit": 20
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
  `blocked`, `idle`, `Paused`, `Done` do not count. `instance_limit` caps the total.
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
