package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNumstat(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAdded   int
		wantRemoved int
	}{
		{
			name:        "empty output",
			input:       "",
			wantAdded:   0,
			wantRemoved: 0,
		},
		{
			name:        "single file",
			input:       "3\t1\tfoo.go\n",
			wantAdded:   3,
			wantRemoved: 1,
		},
		{
			name:        "multiple files sum correctly",
			input:       "3\t1\tfoo.go\n10\t2\tbar/baz.go\n",
			wantAdded:   13,
			wantRemoved: 3,
		},
		{
			name:        "binary files are skipped",
			input:       "5\t0\tfoo.go\n-\t-\timage.png\n2\t2\tbar.go\n",
			wantAdded:   7,
			wantRemoved: 2,
		},
		{
			name:        "path with tabs is preserved via SplitN",
			input:       "4\t4\tpath\twith\ttabs.go\n",
			wantAdded:   4,
			wantRemoved: 4,
		},
		{
			name:        "trailing newlines do not add garbage",
			input:       "1\t0\ta.go\n\n\n",
			wantAdded:   1,
			wantRemoved: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdded, gotRemoved := parseNumstat(tt.input)
			if gotAdded != tt.wantAdded || gotRemoved != tt.wantRemoved {
				t.Errorf("parseNumstat(%q) = (%d, %d), want (%d, %d)",
					tt.input, gotAdded, gotRemoved, tt.wantAdded, tt.wantRemoved)
			}
		})
	}
}

// commitFile writes content to name and commits it, returning the new commit's SHA.
func commitFile(t *testing.T, repo, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustRunGit(t, repo, "add", name)
	mustRunGit(t, repo, "commit", "-q", "-m", name)
	return strings.TrimSpace(mustRunGit(t, repo, "rev-parse", "HEAD"))
}

// An agent that rebases onto origin/main must not be charged with everything that
// landed upstream since its worktree was created.
func TestDiffCountsOnlyChangesSinceUpstreamMergeBase(t *testing.T) {
	repo := t.TempDir()
	mustRunGit(t, "", "init", "-q", "-b", "main", repo)
	mustRunGit(t, repo, "config", "user.name", "Test User")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")

	created := commitFile(t, repo, "a.txt", "a\n")
	commitFile(t, repo, "upstream.txt", "1\n2\n3\n4\n5\n")
	upstream := commitFile(t, repo, "upstream2.txt", "1\n2\n3\n")
	mustRunGit(t, repo, "update-ref", "refs/remotes/origin/main", upstream)
	commitFile(t, repo, "mine.txt", "mine\n")

	g := &GitWorktree{repoPath: repo, worktreePath: repo, baseCommitSHA: created, baseRef: "origin/main"}
	stats := g.DiffNumstat()
	if stats.Error != nil {
		t.Fatal(stats.Error)
	}
	if stats.Added != 1 || stats.Removed != 0 {
		t.Fatalf("rebased onto upstream: got +%d -%d, want +1 -0", stats.Added, stats.Removed)
	}
	if full := g.Diff(); full.Added != 1 {
		t.Fatalf("Diff: got +%d, want +1", full.Added)
	}

	// A stale upstream ref (older than the creation commit) falls back to the
	// creation commit.
	mustRunGit(t, repo, "update-ref", "refs/remotes/origin/main", strings.TrimSpace(mustRunGit(t, repo, "rev-list", "--max-parents=0", "HEAD")))
	g.baseCommitSHA = upstream
	stats = g.DiffNumstat()
	if stats.Added != 1 {
		t.Fatalf("stale upstream: got +%d, want +1", stats.Added)
	}
}
