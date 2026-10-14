package git

import (
	"strconv"
	"strings"
)

// DiffStats holds statistics about the changes in a diff
type DiffStats struct {
	// Content is the full diff content
	Content string
	// Added is the number of added lines
	Added int
	// Removed is the number of removed lines
	Removed int
	// Error holds any error that occurred during diff computation
	// This allows propagating setup errors (like missing base commit) without breaking the flow
	Error error
}

func (d *DiffStats) IsEmpty() bool {
	return d.Added == 0 && d.Removed == 0 && d.Content == ""
}

// Diff returns the git diff between the worktree and the base branch along with statistics
func (g *GitWorktree) Diff() *DiffStats {
	stats := &DiffStats{}

	// -N stages untracked files (intent to add), including them in the diff
	_, err := g.runGitCommand(g.worktreePath, "add", "-N", ".")
	if err != nil {
		stats.Error = err
		return stats
	}

	content, err := g.runGitCommand(g.worktreePath, "--no-pager", "diff", g.diffBase())
	if err != nil {
		stats.Error = err
		return stats
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			stats.Added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			stats.Removed++
		}
	}
	stats.Content = content

	return stats
}

// diffBase is the commit the worktree's changes are measured from. Agents rebase their
// branch onto origin's main branch (or check it out once their PR merged), which moves
// HEAD far past the commit the worktree was created from; a diff against that commit
// would count every upstream change since. So when the merge base of HEAD and the
// upstream branch is newer than the creation commit, diff against the merge base: the
// worktree's own changes, as a PR would show them.
func (g *GitWorktree) diffBase() string {
	base := g.GetBaseCommitSHA()
	upstream := "origin/HEAD"
	if strings.HasPrefix(g.baseRef, "origin/") {
		upstream = g.baseRef
	}
	out, err := g.runGitCommand(g.worktreePath, "merge-base", "HEAD", upstream)
	if err != nil {
		return base
	}
	mergeBase := strings.TrimSpace(out)
	if base == "" {
		return mergeBase
	}
	if _, err := g.runGitCommand(g.worktreePath, "merge-base", "--is-ancestor", base, mergeBase); err != nil {
		// The merge base is older than (or unrelated to) the creation commit, e.g.
		// origin's ref is stale: the creation commit is the better base.
		return base
	}
	return mergeBase
}

// DiffNumstat returns the added/removed line counts between the worktree and the
// base branch without loading the full diff content into memory. Use this when
// only the summary counts are needed (e.g. for unselected instances in the list).
func (g *GitWorktree) DiffNumstat() *DiffStats {
	stats := &DiffStats{}

	// -N stages untracked files (intent to add), including them in the diff
	_, err := g.runGitCommand(g.worktreePath, "add", "-N", ".")
	if err != nil {
		stats.Error = err
		return stats
	}

	out, err := g.runGitCommand(g.worktreePath, "--no-pager", "diff", "--numstat", g.diffBase())
	if err != nil {
		stats.Error = err
		return stats
	}

	stats.Added, stats.Removed = parseNumstat(out)
	return stats
}

// parseNumstat sums the added/removed columns from `git diff --numstat` output.
// Each line is formatted as <added>\t<removed>\t<path>. Binary files report
// "-\t-\t<path>" and are ignored for line totals.
func parseNumstat(out string) (added int, removed int) {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		a, aerr := strconv.Atoi(fields[0])
		r, rerr := strconv.Atoi(fields[1])
		if aerr != nil || rerr != nil {
			continue
		}
		added += a
		removed += r
	}
	return added, removed
}
