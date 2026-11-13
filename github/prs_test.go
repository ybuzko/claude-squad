package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildQueryAliasesEachPR(t *testing.T) {
	q := buildQuery([]PRRef{{Repo: "loop-payments/backend", Number: 7}, {Repo: "o/r", Number: 9}})
	assert.Contains(t, q, `p0: repository(owner: "loop-payments", name: "backend") { pullRequest(number: 7)`)
	assert.Contains(t, q, `p1: repository(owner: "o", name: "r") { pullRequest(number: 9)`)
}

func TestParseResponseReadsStateAndChecks(t *testing.T) {
	refs := []PRRef{{URL: "u0"}, {URL: "u1"}, {URL: "gone"}}
	body := []byte(`{"data":{
	  "p0":{"pullRequest":{"state":"OPEN","mergedAt":null,"headRefOid":"abc",
	    "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
	      {"__typename":"CheckRun","name":"Run Unit Tests","status":"COMPLETED","conclusion":"FAILURE"},
	      {"__typename":"CheckRun","name":"check-tsc","status":"COMPLETED","conclusion":"TIMED_OUT"},
	      {"__typename":"CheckRun","name":"claude","status":"COMPLETED","conclusion":"SKIPPED"},
	      {"__typename":"CheckRun","name":"old","status":"COMPLETED","conclusion":"CANCELLED"},
	      {"__typename":"CheckRun","name":"Run E2E Tests","status":"IN_PROGRESS","conclusion":null},
	      {"__typename":"StatusContext","context":"ci/legacy","state":"ERROR"},
	      {"__typename":"StatusContext","context":"ci/wait","state":"PENDING"}]}}}}]}}},
	  "p1":{"pullRequest":{"state":"MERGED","mergedAt":"2026-11-12T10:00:00Z","headRefOid":"def",
	    "commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}},
	  "p2":{"pullRequest":null}},
	 "errors":[{"message":"Could not resolve to a PullRequest with the number of 3."}]}`)
	out := map[string]PRState{}
	errs, err := parseResponse(body, refs, out)
	require.NoError(t, err)
	assert.Equal(t, []string{"Could not resolve to a PullRequest with the number of 3."}, errs)

	assert.Equal(t, PRState{HeadSHA: "abc", Failed: []string{"Run Unit Tests", "check-tsc", "ci/legacy"}, Running: 2}, out["u0"])
	assert.Equal(t, PRState{Merged: true, MergedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), HeadSHA: "def"}, out["u1"])
	_, ok := out["gone"]
	assert.False(t, ok)
}
