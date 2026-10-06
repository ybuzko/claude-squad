package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewIssuesPaginatesAndSendsBareAPIKey(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("Authorization"))
		var req graphqlRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		calls = append(calls, req.Variables)

		page := map[string]any{
			"data": map[string]any{"customView": map[string]any{"issues": map[string]any{
				"nodes": []map[string]any{{
					"id": "uuid-1", "identifier": "TSA-1", "title": "First", "url": "https://l/1",
					"dueDate": "2026-10-05",
					"state":   map[string]any{"id": "s1", "name": "Todo", "type": "unstarted"},
				}},
				"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"},
			}}},
		}
		if _, hasCursor := req.Variables["after"]; hasCursor {
			page = map[string]any{
				"data": map[string]any{"customView": map[string]any{"issues": map[string]any{
					"nodes": []map[string]any{{
						"id": "uuid-2", "identifier": "TSA-2", "title": "Second", "url": "https://l/2",
						"state": map[string]any{"id": "s2", "name": "Done", "type": "completed"},
					}},
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				}}},
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(page))
	}))
	defer srv.Close()

	c := NewClientWithEndpoint("test-key", srv.URL)
	issues, err := c.ViewIssues(context.Background(), "view-1")
	require.NoError(t, err)

	require.Len(t, calls, 2)
	assert.Equal(t, "view-1", calls[0]["viewId"])
	assert.Equal(t, "c1", calls[1]["after"])

	require.Len(t, issues, 2)
	assert.Equal(t, Issue{ID: "uuid-1", Identifier: "TSA-1", Title: "First", URL: "https://l/1",
		StateID: "s1", StateName: "Todo", StateType: "unstarted", DueDate: "2026-10-05"}, issues[0])
	assert.Equal(t, "TSA-2", issues[1].Identifier)
	assert.Empty(t, issues[1].DueDate, "no dueDate in the payload: empty")
}

func TestViewIssuesMissingView(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"customView":null}}`))
	}))
	defer srv.Close()

	_, err := NewClientWithEndpoint("k", srv.URL).ViewIssues(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestGraphQLErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
	}))
	defer srv.Close()

	_, err := NewClientWithEndpoint("k", srv.URL).IssuesByID(context.Background(), []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authentication required")
}

func TestIssuesByIDSkipsEmptyInput(t *testing.T) {
	c := NewClientWithEndpoint("k", "http://127.0.0.1:1")
	issues, err := c.IssuesByID(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, issues)
}

func TestDoneRule(t *testing.T) {
	rule := DoneRule{StateTypes: []string{"completed", "canceled"}, StateIDs: []string{"custom-done"}}
	assert.True(t, rule.IsDone(Issue{StateType: "completed"}))
	assert.True(t, rule.IsDone(Issue{StateType: "canceled"}))
	assert.True(t, rule.IsDone(Issue{StateType: "started", StateID: "custom-done"}))
	assert.False(t, rule.IsDone(Issue{StateType: "started", StateID: "other"}))
}

func TestNewIssuesPreservesViewOrder(t *testing.T) {
	view := []Issue{{Identifier: "TSA-3"}, {Identifier: "TSA-1"}, {Identifier: "TSA-2"}}
	existing := map[string]bool{"TSA-1": true}
	got := NewIssues(view, func(id string) bool { return existing[id] })
	require.Len(t, got, 2)
	assert.Equal(t, "TSA-3", got[0].Identifier)
	assert.Equal(t, "TSA-2", got[1].Identifier)
}
