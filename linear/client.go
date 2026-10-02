// Package linear is a read-only client for the Linear GraphQL API. The dispatcher
// only ever reads from Linear: it discovers tickets in a view and watches their
// workflow state. It never assigns, transitions, or comments.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultEndpoint = "https://api.linear.app/graphql"

// Issue is the subset of a Linear issue the dispatcher cares about.
type Issue struct {
	// ID is the Linear UUID. Stable across renames; used for state lookups.
	ID string
	// Identifier is the human key, e.g. "TSA-123". Used as the instance title.
	Identifier string
	Title      string
	URL        string
	StateID    string
	StateName  string
	// StateType is one of Linear's workflow state types: triage, backlog,
	// unstarted, started, completed, canceled.
	StateType string
}

type Client struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:   apiKey,
		endpoint: DefaultEndpoint,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientWithEndpoint is for tests.
func NewClientWithEndpoint(apiKey, endpoint string) *Client {
	c := NewClient(apiKey)
	c.endpoint = endpoint
	return c
}

type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphqlError struct {
	Message string `json:"message"`
}

type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors"`
}

func (c *Client) do(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Personal API keys are sent bare, without a "Bearer " prefix.
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear returned HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var gql graphqlResponse
	if err := json.Unmarshal(raw, &gql); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(gql.Errors) > 0 {
		return fmt.Errorf("linear graphql error: %s", gql.Errors[0].Message)
	}
	if err := json.Unmarshal(gql.Data, out); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

type issueNode struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
}

func (n issueNode) toIssue() Issue {
	return Issue{
		ID:         n.ID,
		Identifier: n.Identifier,
		Title:      n.Title,
		URL:        n.URL,
		StateID:    n.State.ID,
		StateName:  n.State.Name,
		StateType:  n.State.Type,
	}
}

type issueConnection struct {
	Nodes    []issueNode `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

const issueFields = `id identifier title url state { id name type }`

const viewIssuesQuery = `query ViewIssues($viewId: String!, $after: String) {
  customView(id: $viewId) {
    issues(first: 50, after: $after) {
      nodes { ` + issueFields + ` }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

// ViewIssues returns every issue currently in the custom view, in view order.
func (c *Client) ViewIssues(ctx context.Context, viewID string) ([]Issue, error) {
	var all []Issue
	var after *string
	for {
		vars := map[string]any{"viewId": viewID}
		if after != nil {
			vars["after"] = *after
		}
		var data struct {
			CustomView *struct {
				Issues issueConnection `json:"issues"`
			} `json:"customView"`
		}
		if err := c.do(ctx, viewIssuesQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.CustomView == nil {
			return nil, fmt.Errorf("linear view %q not found or not accessible", viewID)
		}
		for _, n := range data.CustomView.Issues.Nodes {
			all = append(all, n.toIssue())
		}
		if !data.CustomView.Issues.PageInfo.HasNextPage {
			return all, nil
		}
		cursor := data.CustomView.Issues.PageInfo.EndCursor
		after = &cursor
	}
}

const issuesByIDQuery = `query IssuesByID($ids: [ID!]!) {
  issues(first: 250, filter: { id: { in: $ids } }) {
    nodes { ` + issueFields + ` }
  }
}`

// IssuesByID looks up issues by Linear UUID. Missing ids are simply absent from
// the result.
func (c *Client) IssuesByID(ctx context.Context, ids []string) ([]Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var data struct {
		Issues issueConnection `json:"issues"`
	}
	if err := c.do(ctx, issuesByIDQuery, map[string]any{"ids": ids}, &data); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(data.Issues.Nodes))
	for _, n := range data.Issues.Nodes {
		out = append(out, n.toIssue())
	}
	return out, nil
}

const issueByIdentifierQuery = `query IssueByIdentifier($id: String!) {
  issue(id: $id) { ` + issueFields + ` }
}`

// IssueByIdentifier looks up a single issue by its human key ("TSA-123") or UUID.
func (c *Client) IssueByIdentifier(ctx context.Context, identifier string) (Issue, error) {
	var data struct {
		Issue *issueNode `json:"issue"`
	}
	if err := c.do(ctx, issueByIdentifierQuery, map[string]any{"id": identifier}, &data); err != nil {
		return Issue{}, err
	}
	if data.Issue == nil {
		return Issue{}, fmt.Errorf("linear issue %q not found", identifier)
	}
	return data.Issue.toIssue(), nil
}

// DoneRule decides whether an issue's workflow state counts as finished.
type DoneRule struct {
	StateTypes []string
	StateIDs   []string
}

func (r DoneRule) IsDone(issue Issue) bool {
	for _, t := range r.StateTypes {
		if t == issue.StateType {
			return true
		}
	}
	for _, id := range r.StateIDs {
		if id == issue.StateID {
			return true
		}
	}
	return false
}

// NewIssues returns the view issues that have no instance yet, preserving view
// order. hasInstance is keyed by identifier (the instance title).
func NewIssues(view []Issue, hasInstance func(identifier string) bool) []Issue {
	var out []Issue
	for _, is := range view {
		if !hasInstance(is.Identifier) {
			out = append(out, is)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
