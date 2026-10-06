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
| Persistence | `session/storage.go` `InstanceData` → `~/.claude-squad/state.json` `instances[]` | mirrors the new fields |
| Instance creation | `app/app.go` `KeyNew`: `session.NewInstance` → `list.AddInstance` (finalizer) → `SetStatus(Loading)` → finalizer → background `Start(true)` → `instanceStartedMsg` | `app/dispatch.go` reuses exactly this path; `instanceStartedMsg.autoSpawned` suppresses focus steal + help overlay |
| Tick loop | `app/app.go`: `previewTickMsg` (100 ms, redraw) and `tickUpdateMetadataCmd` → `metadataUpdateDoneMsg` (500 ms; `HasUpdated()` + git diff per instance in goroutines, then sets `Running`/`Ready` on the main thread) | status-file read rides the 500 ms tick; `metadataUpdateDoneMsg` prefers hook state over the pane-diff heuristic; a third chained cmd (`linearPollTickMsg`) polls Linear |
| Status heuristic | `session/tmux/tmux.go` `HasUpdated()` — sha256 of `capture-pane`; changed → `Running`, else `Ready` | unchanged; used only for instances without a status file |
| tmux session | `tmux new-session -d -s claudesquad_<title> -c <worktree> <program>` | `-e CS_ISSUE_ID=<id>` added for ticket instances |
| Worktree + branch | `session/git/worktree.go`: branch = `BranchPrefix + title` (lowercased by `sanitizeBranchName`), path `~/.claude-squad/worktrees/<branch>_<hex>`, base = `HEAD` of repo | `NewGitWorktreeForBranch(...)` takes an explicit branch + base ref (`origin/main`, fetched first) |
| Pause / Resume / Kill | `Instance.Pause()` keeps branch + path; `Resume()` restarts tmux with the same `Program`; `Kill()` drops worktree **and** branch | ticket instances: `D` pauses (branch kept); `D` on Paused/Done kills; `Resume()` uses `claude --resume <session>` / `claude -c` |
| Config | `config/config.go`, `~/.claude-squad/config.json`, `cs debug` | `linear`, `spawn`, `instance_limit` sections |
| List rendering | `ui/list.go` `List.items` (user-ordered via J/K), `InstanceRenderer.Render` glyph by status, header `" Instances "` | urgency sort (blocked → idle → running → paused → done), header counts |
| Trust prompt | `Instance.CheckAndHandleTrustPrompt()` exists upstream but is never called | called on a short backoff after auto-spawn |
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
Claude Code did **not** show a trust dialog for worktrees of an already-trusted repo;
the auto-dismiss stays in as a safety net.

## Keys added or changed in Linear mode

| Key | Behaviour |
|---|---|
| `t` | Prompt for an issue id (any ticket, in the view or not) and spawn it like the poller would |
| `D` | On a live ticket session: **pause** (tmux killed, worktree removed, branch and Claude conversation kept). On a paused or done one: delete worktree + branch |
| `r` | Resume a paused ticket session with `claude --resume <session>` (or `claude -c`), so the conversation continues |
| `n` / `N` | Unchanged — plain sessions for non-ticket work, created from `spawn.repo_path` |

Deleting a ticket session whose issue is still in the view makes the poller respawn
it fresh on the next poll; that is the "start over" path.

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
    "view_id": "1f3cf867-a139-4c52-a6b3-2023b8bfeeb1",
    "poll_interval_sec": 60,
    "max_concurrent": 5,
    "done_state_types": ["completed", "canceled"],
    "done_state_ids": []
  },
  "spawn": {
    "program": "claude \"/triage-linear-ticket {ISSUE_ID}\"",
    "repo_path": "/home/yaroslav/agent1/backend",
    "branch_prefix": "agent/"
  },
  "instance_limit": 20
}
```

- `LINEAR_API_KEY` in the environment overrides `linear.api_key`.
- `view_id` is the UUID in the Linear view URL (`linear.app/<ws>/view/<uuid>`).
- `max_concurrent` caps instances that are actually working (`Running`/`Loading`);
  `blocked`, `idle`, `Paused`, `Done` do not count. `instance_limit` caps the total.
- Done detection: an instance is `Done` when its issue's workflow state has a type in
  `done_state_types`, or an id in `done_state_ids`. A reopened ticket flips back.
- Branch: `agent/<issue-id-lowercase>-<slugified-title>`, from `origin/main`
  (fetched right before the worktree is created).
- Worktree path stays upstream's `~/.claude-squad/worktrees/<branch>_<hex>`; the
  path is recorded on the instance and reused by Pause/Resume, which is what keeps
  `claude --resume` pointed at the right transcript.
