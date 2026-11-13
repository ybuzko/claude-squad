package app

import (
	"claude-squad/config"
	"claude-squad/github"
	"claude-squad/log"
	"claude-squad/session"
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The PR watcher follows the pull requests sessions open and tells the session when
// checks fail on a PR's latest commit (once per commit) or when a PR is merged, the
// same way the Linear update watcher hands over comments: idle sessions get the prompt
// typed in, paused ones are resumed with it, busy or open ones get it on a later poll.
// Sessions find out about their PRs from Claude Code's transcript (see PRScanner), so
// it works for every session in the list, ticket or not.

type prWatcher struct {
	cfg     config.PRWatchConfig
	polling bool
	// scanner is used only by the poll in flight, off the UI goroutine.
	scanner *session.PRScanner
	fetch   func(context.Context, []github.PRRef) (map[string]github.PRState, error)
}

func newPRWatcher(cfg *config.Config) *prWatcher {
	if !cfg.PRWatch.Enabled {
		return nil
	}
	return &prWatcher{cfg: cfg.PRWatch, scanner: session.NewPRScanner(), fetch: github.FetchPRs}
}

type prPollTickMsg struct{}

// prPollDoneMsg carries the PRs found in each session's transcripts and the current
// state of every open one.
type prPollDoneMsg struct {
	found  map[*session.Instance][]session.PullRequest
	states map[string]github.PRState
}

// prNews is one thing to tell a session about one of its PRs.
type prNews struct {
	url    string
	merged bool
	sha    string // the failing commit, when not merged
	text   string
}

// prNewsSentMsg reports news typed into an idle session; see ticketUpdateSentMsg.
type prNewsSentMsg struct {
	instance *session.Instance
	news     []prNews
	skipped  bool
	err      error
}

func (m *home) schedulePRPollCmd() tea.Cmd {
	w := m.prWatch
	if w == nil {
		return nil
	}
	interval := time.Duration(w.cfg.PollIntervalSec) * time.Second
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(interval):
			return prPollTickMsg{}
		}
	}
}

// runPRPollCmd scans the transcripts of the sessions in the list for new PRs and asks
// GitHub about every PR that is not closed yet, in the background.
func (m *home) runPRPollCmd() tea.Cmd {
	w := m.prWatch
	if w == nil || w.polling {
		return nil
	}
	w.polling = true

	type target struct {
		inst *session.Instance
		dir  string
		prs  []session.PullRequest
	}
	var targets []target
	seeded := false
	now := time.Now()
	for _, inst := range m.list.GetInstances() {
		dir := inst.GetWorktreePath()
		if dir == "" {
			continue
		}
		if inst.PRWatchSince.IsZero() {
			// PRs merged before the watcher first looked are old news.
			inst.PRWatchSince = now
			seeded = true
		}
		targets = append(targets, target{inst: inst, dir: dir, prs: append([]session.PullRequest(nil), inst.PRs...)})
	}
	if seeded {
		if err := m.saveInstances(); err != nil {
			log.ErrorLog.Printf("saving instances: %v", err)
		}
	}

	return func() tea.Msg {
		found := make(map[*session.Instance][]session.PullRequest)
		var refs []github.PRRef
		queued := make(map[string]bool)
		for _, t := range targets {
			prs, err := w.scanner.Scan(t.dir)
			if err != nil {
				log.WarningLog.Printf("%s: reading transcripts for PRs: %v", t.inst.Title, err)
			}
			found[t.inst] = prs
			closed := make(map[string]bool)
			for _, pr := range t.prs {
				closed[pr.URL] = pr.Closed
			}
			for _, pr := range prs {
				if !closed[pr.URL] && !queued[pr.URL] {
					queued[pr.URL] = true
					refs = append(refs, github.PRRef{Repo: pr.Repo, Number: pr.Number, URL: pr.URL})
				}
			}
		}
		var states map[string]github.PRState
		if len(refs) > 0 {
			ctx, cancel := context.WithTimeout(m.ctx, 60*time.Second)
			defer cancel()
			var err error
			// Best effort, like the comment lookup: what came back is used, the rest is
			// asked again next poll.
			if states, err = w.fetch(ctx, refs); err != nil {
				log.WarningLog.Printf("PR lookup: %v", err)
			}
		}
		return prPollDoneMsg{found: found, states: states}
	}
}

// handlePRPollDone records newly found PRs and hands each session its PR news. It
// always reschedules.
func (m *home) handlePRPollDone(msg prPollDoneMsg) tea.Cmd {
	m.prWatch.polling = false
	var cmds []tea.Cmd
	persist := false
	for _, inst := range m.list.GetInstances() {
		if addPRs(inst, msg.found[inst]) {
			persist = true
		}
		news, changed := m.prNewsFor(inst, msg.states)
		persist = persist || changed
		if len(news) == 0 || m.delivering[inst] || inst.Restoring || inst.Pausing ||
			inst.Status == session.Loading || inst.Attached() {
			continue
		}
		texts := make([]string, len(news))
		for k, n := range news {
			texts[k] = n.text
		}
		prompt := strings.Join(texts, " ")
		if inst.Paused() {
			markPRNews(inst, news)
			inst.NewsAt = time.Now()
			persist = true
			cmds = append(cmds, m.resumeInBackground(inst, prompt, prNewsReason(news)))
			continue
		}
		if !acceptsPrompt(inst) {
			continue
		}
		m.delivering[inst] = true
		cmds = append(cmds, func() tea.Msg {
			empty, err := inst.InputBoxEmpty()
			if err != nil || !empty {
				return prNewsSentMsg{instance: inst, skipped: true}
			}
			return prNewsSentMsg{instance: inst, news: news, err: inst.SendPrompt(prompt)}
		})
	}
	if persist {
		if err := m.saveInstances(); err != nil {
			cmds = append(cmds, m.handleError(err))
		}
	}
	return tea.Batch(append(cmds, m.schedulePRPollCmd())...)
}

func (m *home) handlePRNewsSent(msg prNewsSentMsg) tea.Cmd {
	inst := msg.instance
	delete(m.delivering, inst)
	if msg.skipped {
		return nil
	}
	if msg.err != nil {
		return m.handleError(fmt.Errorf("sending PR news to %s: %w", inst.Title, msg.err))
	}
	log.InfoLog.Printf("%s: sent PR news (%s)", inst.Title, prNewsReason(msg.news))
	markPRNews(inst, msg.news)
	inst.NewsAt = time.Now()
	if err := m.saveInstances(); err != nil {
		return m.handleError(err)
	}
	return nil
}

// addPRs appends the PRs the session does not know yet; it reports whether any were.
func addPRs(inst *session.Instance, found []session.PullRequest) bool {
	added := false
	for _, pr := range found {
		known := false
		for _, have := range inst.PRs {
			known = known || have.URL == pr.URL
		}
		if !known {
			log.InfoLog.Printf("%s: watching %s", inst.Title, pr.URL)
			inst.PRs = append(inst.PRs, pr)
			added = true
		}
	}
	return added
}

// prNewsFor works out what to tell a session about its PRs. PRs closed without merging,
// or merged before the watcher started, are marked closed on the spot; it reports
// whether it marked any.
func (m *home) prNewsFor(inst *session.Instance, states map[string]github.PRState) (news []prNews, changed bool) {
	cfg := m.prWatch.cfg
	for k := range inst.PRs {
		pr := &inst.PRs[k]
		st, ok := states[pr.URL]
		if pr.Closed || !ok {
			continue
		}
		switch {
		case st.Merged && st.MergedAt.After(inst.PRWatchSince):
			news = append(news, prNews{url: pr.URL, merged: true,
				text: strings.ReplaceAll(cfg.MergedPrompt, "{PR_URL}", pr.URL)})
		case st.Merged || st.Closed:
			pr.Closed = true
			changed = true
		case len(st.Failed) > 0 && st.HeadSHA != pr.FailedSHA:
			checks := strings.Join(st.Failed, ", ")
			if st.Running > 0 {
				checks += fmt.Sprintf(" failed, %d still running", st.Running)
			} else {
				checks += " failed"
			}
			sha := st.HeadSHA
			r := strings.NewReplacer("{PR_URL}", pr.URL, "{SHA}", sha[:min(len(sha), 10)], "{CHECKS}", checks)
			news = append(news, prNews{url: pr.URL, sha: sha, text: r.Replace(cfg.CIFailedPrompt)})
		}
	}
	return news, changed
}

// markPRNews records news as told, so it is not sent again.
func markPRNews(inst *session.Instance, news []prNews) {
	for _, n := range news {
		for k := range inst.PRs {
			pr := &inst.PRs[k]
			if pr.URL != n.url {
				continue
			}
			if n.merged {
				pr.Closed = true
			} else {
				pr.FailedSHA = n.sha
			}
		}
	}
}

func prNewsReason(news []prNews) string {
	parts := make([]string, len(news))
	for k, n := range news {
		what := "CI failed"
		if n.merged {
			what = "merged"
		}
		parts[k] = n.url + " " + what
	}
	return strings.Join(parts, ", ")
}
