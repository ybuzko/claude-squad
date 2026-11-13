package app

import (
	"claude-squad/github"
	"claude-squad/session"
	"claude-squad/status"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPRHome(t *testing.T) *home {
	h := newPollHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the rescheduled poll returns at once
	h.ctx = ctx
	h.prWatch = &prWatcher{cfg: h.appConfig.PRWatch}
	h.prWatch.cfg.CIFailedPrompt = "failed {PR_URL} {SHA}: {CHECKS}."
	h.prWatch.cfg.MergedPrompt = "merged {PR_URL}."
	return h
}

// runAll runs cmd and any batch it returns, collecting the messages.
func runAll(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, runAll(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

func pr(n string) session.PullRequest {
	return session.PullRequest{URL: "https://github.com/o/r/pull/" + n, Repo: "o/r"}
}

func TestPRWatchResumesPausedSessionWithNewsOnce(t *testing.T) {
	h := newPRHome(t)
	since := time.Now().Add(-time.Hour)
	inst := ticket("TSA-1", session.Paused, true)
	inst.PRWatchSince = since
	inst.PRs = []session.PullRequest{pr("1")}
	h.list.AddInstance(inst)

	failing := github.PRState{HeadSHA: "abcdef0123456789", Failed: []string{"Run Unit Tests", "check-tsc"}, Running: 2}
	states := map[string]github.PRState{
		pr("1").URL: failing,
		pr("2").URL: {Merged: true, MergedAt: since.Add(time.Minute)},
		pr("3").URL: {Merged: true, MergedAt: since.Add(-time.Minute)},
	}
	msgs := runAll(h.handlePRPollDone(prPollDoneMsg{
		found:  map[*session.Instance][]session.PullRequest{inst: {pr("1"), pr("2"), pr("3")}},
		states: states,
	}))

	require.Len(t, msgs, 1)
	resumed := msgs[0].(resumeDoneMsg)
	assert.Equal(t, "failed "+pr("1").URL+" abcdef0123: Run Unit Tests, check-tsc failed, 2 still running. merged "+pr("2").URL+".",
		resumed.prompt)
	assert.True(t, inst.Restoring)
	assert.False(t, inst.NewsAt.IsZero(), "the row shows that news went in")
	assert.Equal(t, []session.PullRequest{
		{URL: pr("1").URL, Repo: "o/r", FailedSHA: failing.HeadSHA},
		{URL: pr("2").URL, Repo: "o/r", Closed: true},
		{URL: pr("3").URL, Repo: "o/r", Closed: true}, // merged before the watcher looked: old news
	}, inst.PRs)

	// The same state is not news again; a new failing commit is.
	inst.Restoring = false
	assert.Empty(t, runAll(h.handlePRPollDone(prPollDoneMsg{states: states})))
	states[pr("1").URL] = github.PRState{HeadSHA: "fedcba", Failed: []string{"Run Unit Tests"}}
	msgs = runAll(h.handlePRPollDone(prPollDoneMsg{states: states}))
	require.Len(t, msgs, 1)
	assert.Equal(t, "failed "+pr("1").URL+" fedcba: Run Unit Tests failed.", msgs[0].(resumeDoneMsg).prompt)
}

func TestPRWatchWaitsForABusySession(t *testing.T) {
	h := newPRHome(t)
	inst := ticket("TSA-1", session.Running, true)
	inst.PRs = []session.PullRequest{pr("1")}
	h.list.AddInstance(inst)
	require.NoError(t, status.Write("TSA-1", status.Status{State: status.StateRunning}))

	states := map[string]github.PRState{pr("1").URL: {HeadSHA: "abc", Failed: []string{"x"}}}
	assert.Empty(t, runAll(h.handlePRPollDone(prPollDoneMsg{states: states})))
	assert.Empty(t, inst.PRs[0].FailedSHA, "told on a later poll, once the session is idle")
	assert.False(t, h.prWatch.polling)
}

// Sessions made by hand have no hook status; an idle pane is enough.
func TestPRWatchTypesNewsIntoAnIdleSession(t *testing.T) {
	h := newPRHome(t)
	inst := &session.Instance{Title: "manual", Status: session.Ready, PRs: []session.PullRequest{pr("1")}}
	h.list.AddInstance(inst)
	states := map[string]github.PRState{pr("1").URL: {HeadSHA: "abc", Failed: []string{"x"}}}

	msgs := runAll(h.handlePRPollDone(prPollDoneMsg{states: states}))
	require.Len(t, msgs, 1)
	sent := msgs[0].(prNewsSentMsg)
	assert.True(t, sent.skipped, "not running here, so the input-box check fails")
	assert.True(t, h.delivering[inst])
	assert.Empty(t, runAll(h.handlePRPollDone(prPollDoneMsg{states: states})), "one delivery at a time")

	h.handlePRNewsSent(sent)
	assert.False(t, h.delivering[inst])
	assert.Empty(t, inst.PRs[0].FailedSHA)

	news, _ := h.prNewsFor(inst, states)
	h.handlePRNewsSent(prNewsSentMsg{instance: inst, news: news})
	assert.Equal(t, "abc", inst.PRs[0].FailedSHA)
	assert.False(t, inst.NewsAt.IsZero())
}

func TestPRPollFindsPRsInTranscriptsAndSkipsClosedOnes(t *testing.T) {
	h := newPRHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	worktree := filepath.Join(t.TempDir(), "wt")
	inst, err := session.FromInstanceData(session.InstanceData{
		Title: "TSA-1", IssueID: "TSA-1", Status: session.Paused,
		Worktree: session.GitWorktreeData{WorktreePath: worktree, BranchName: "b"},
		PRs:      []session.PullRequest{{URL: pr("2").URL, Repo: "o/r", Number: 2, Closed: true}},
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	h.list.AddInstance(&session.Instance{Title: "no worktree"})

	dir, err := session.ClaudeProjectDir(worktree)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	line := func(n string) string {
		return `{"type":"pr-link","prNumber":` + n + `,"prUrl":"` + pr(n).URL + `","prRepository":"o/r"}` + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.jsonl"), []byte(line("1")+line("2")), 0o644))

	var asked []github.PRRef
	h.prWatch.scanner = session.NewPRScanner()
	h.prWatch.fetch = func(_ context.Context, refs []github.PRRef) (map[string]github.PRState, error) {
		asked = refs
		return map[string]github.PRState{pr("1").URL: {HeadSHA: "abc"}}, nil
	}
	cmd := h.runPRPollCmd()
	assert.False(t, inst.PRWatchSince.IsZero(), "watched from now on")
	assert.Nil(t, h.runPRPollCmd(), "one poll at a time")

	done := cmd().(prPollDoneMsg)
	assert.Equal(t, []github.PRRef{{Repo: "o/r", Number: 1, URL: pr("1").URL}}, asked, "closed PRs are not looked up")
	assert.Empty(t, runAll(h.handlePRPollDone(done)), "passing CI is not news")
	assert.False(t, h.prWatch.polling)
	require.Len(t, inst.PRs, 2)
	assert.Equal(t, 1, inst.PRs[1].Number)
}
