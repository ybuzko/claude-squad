// Package github looks up the state of pull requests through the gh CLI, which
// carries the user's GitHub login.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// PRRef names a pull request.
type PRRef struct {
	Repo   string // owner/name
	Number int
	URL    string
}

// PRState is what the PR watcher needs to know about a pull request.
type PRState struct {
	Merged   bool
	MergedAt time.Time
	// Closed is true for a PR closed without merging.
	Closed  bool
	HeadSHA string
	// Failed names the checks that failed on the head commit; Running counts the ones
	// still going.
	Failed  []string
	Running int
}

// batchSize keeps each GraphQL query well inside GitHub's complexity limits.
const batchSize = 20

// FetchPRs returns the state of each PR, keyed by URL. A PR GitHub cannot find is left
// out; the error reports it alongside any failed batch.
func FetchPRs(ctx context.Context, refs []PRRef) (map[string]PRState, error) {
	out := make(map[string]PRState, len(refs))
	var errs []string
	for start := 0; start < len(refs); start += batchSize {
		batch := refs[start:min(start+batchSize, len(refs))]
		if err := fetchBatch(ctx, batch, out); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

func fetchBatch(ctx context.Context, refs []PRRef, out map[string]PRState) error {
	query := buildQuery(refs)
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-f", "query="+query)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	// GraphQL errors (a deleted PR, say) exit non-zero but still return the rest.
	errs, err := parseResponse(stdout, refs, out)
	if err != nil {
		if runErr != nil {
			return fmt.Errorf("gh api graphql: %v: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("gh api graphql: %s", strings.Join(errs, "; "))
	}
	return nil
}

const prFields = `state mergedAt headRefOid
      commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { nodes {
        __typename
        ... on CheckRun { name status conclusion }
        ... on StatusContext { context state }
      } } } } } }`

func buildQuery(refs []PRRef) string {
	var b strings.Builder
	b.WriteString("{")
	for k, r := range refs {
		owner, name, _ := strings.Cut(r.Repo, "/")
		fmt.Fprintf(&b, "\n  p%d: repository(owner: %q, name: %q) { pullRequest(number: %d) { %s } }",
			k, owner, name, r.Number, prFields)
	}
	b.WriteString("\n}")
	return b.String()
}

type prNode struct {
	State      string    `json:"state"`
	MergedAt   time.Time `json:"mergedAt"`
	HeadRefOid string    `json:"headRefOid"`
	Commits    struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkNode `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type checkNode struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// parseResponse fills out from a GraphQL response and returns its error messages.
func parseResponse(body []byte, refs []PRRef, out map[string]PRState) ([]string, error) {
	var resp struct {
		Data map[string]*struct {
			PullRequest *prNode `json:"pullRequest"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse gh api graphql output: %w", err)
	}
	for k, r := range refs {
		repo := resp.Data[fmt.Sprintf("p%d", k)]
		if repo == nil || repo.PullRequest == nil {
			continue
		}
		out[r.URL] = stateOf(repo.PullRequest)
	}
	var errs []string
	for _, e := range resp.Errors {
		errs = append(errs, e.Message)
	}
	return errs, nil
}

func stateOf(n *prNode) PRState {
	st := PRState{
		Merged:   n.State == "MERGED",
		MergedAt: n.MergedAt,
		Closed:   n.State == "CLOSED",
		HeadSHA:  n.HeadRefOid,
	}
	failed := make(map[string]bool)
	for _, c := range n.Commits.Nodes {
		if c.Commit.StatusCheckRollup == nil {
			continue
		}
		for _, ch := range c.Commit.StatusCheckRollup.Contexts.Nodes {
			switch ch.Typename {
			case "CheckRun":
				switch {
				case ch.Status != "COMPLETED":
					st.Running++
				case ch.Conclusion == "FAILURE" || ch.Conclusion == "TIMED_OUT" || ch.Conclusion == "STARTUP_FAILURE":
					failed[ch.Name] = true
				}
			case "StatusContext":
				switch ch.State {
				case "PENDING", "EXPECTED":
					st.Running++
				case "FAILURE", "ERROR":
					failed[ch.Context] = true
				}
			}
		}
	}
	for name := range failed {
		st.Failed = append(st.Failed, name)
	}
	sort.Strings(st.Failed)
	return st
}
