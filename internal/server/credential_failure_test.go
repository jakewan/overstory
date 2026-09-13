package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/authored"
	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/maintenance"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// credentialFailures are the token-acquisition failures every repository in a call
// shares — gh missing, gh holding no usable token, gh not answering — each as a
// fetch returns it, bare or wrapped by the token fetch.
var credentialFailures = []struct {
	name string
	err  error
	want error
}{
	{"no usable token", github.ErrGHNotAuthed, github.ErrGHNotAuthed},
	{"no usable token, wrapped", fmt.Errorf("obtaining gh token: %w", github.ErrGHNotAuthed), github.ErrGHNotAuthed},
	{"gh not installed", github.ErrGHNotFound, github.ErrGHNotFound},
	{"gh did not respond", fmt.Errorf("obtaining gh token after waiting 10s: %w", github.ErrGHTimedOut), github.ErrGHTimedOut},
}

// TestBatchToolsFailTheCallOnCredentialFailure pins that a credential failure, which
// every repository in a batch shares, surfaces as one error carrying the fix rather
// than a fetch_failed marker per repository — and names no repository, since none is
// the cause.
func TestBatchToolsFailTheCallOnCredentialFailure(t *testing.T) {
	for _, cf := range credentialFailures {
		tools := []struct {
			name    string
			call    func(*testing.T, *mcp.Server, map[string]any) *mcp.CallToolResult
			fetcher fakeFetcher
		}{
			{"authored_activity_batch", callAuthoredActivityBatch, authoredBatchFetcher(map[string]authoredCanned{
				"acme/a": {err: cf.err},
				"acme/b": {err: cf.err},
			})},
			{"maintenance_activity_batch", callMaintenanceActivityBatch, fakeFetcher{eventsByRepo: map[string]eventsCanned{
				"acme/a": {err: cf.err},
				"acme/b": {err: cf.err},
			}}},
		}
		for _, tool := range tools {
			t.Run(tool.name+"/"+cf.name, func(t *testing.T) {
				srv := New(WithFetcher(tool.fetcher), WithClock(func() time.Time { return fixedClock }))

				res := tool.call(t, srv, map[string]any{
					"repos":  []any{"acme/a", "acme/b"},
					"author": "alice",
					"since":  "2026-05-01T00:00:00Z",
				})

				if !res.IsError {
					t.Fatalf("IsError = false, want one whole-call error for %v", cf.err)
				}
				msg := contentText(res)
				if !strings.Contains(msg, cf.want.Error()) {
					t.Errorf("error %q does not carry %q", msg, cf.want)
				}
				for _, repo := range []string{"acme/a", "acme/b"} {
					if strings.Contains(msg, repo) {
						t.Errorf("error %q names repository %s", msg, repo)
					}
				}
			})
		}
	}
}

// TestBatchToolsDegradeTokenFetchCutOffByDeadline pins that a token fetch cut off by
// one repository's own deadline stays that repository's fetch_failed: the deadline
// belongs to the repository, not to the credential.
func TestBatchToolsDegradeTokenFetchCutOffByDeadline(t *testing.T) {
	cutOff := fmt.Errorf("obtaining gh token: %w", context.DeadlineExceeded)
	args := map[string]any{"repos": []any{"acme/a", "acme/b"}, "author": "alice", "since": "2026-05-01T00:00:00Z"}

	t.Run("authored_activity_batch", func(t *testing.T) {
		srv := New(WithFetcher(authoredBatchFetcher(map[string]authoredCanned{
			"acme/a": {err: cutOff},
			"acme/b": {result: sixCounts(1, 0, 0, 0, 0, 0)},
		})), WithClock(func() time.Time { return fixedClock }))

		facts := decodeBatchFacts(t, callAuthoredActivityBatch(t, srv, args))

		if got := facts.Repos[0]; got.Unavailable != authored.UnavailableFetchFailed {
			t.Errorf("acme/a = %+v, want unavailable fetch_failed", got)
		}
		if got := facts.Repos[1]; !got.Available {
			t.Errorf("acme/b = %+v, want available", got)
		}
	})
	t.Run("maintenance_activity_batch", func(t *testing.T) {
		srv := New(WithFetcher(fakeFetcher{eventsByRepo: map[string]eventsCanned{
			"acme/a": {err: cutOff},
			"acme/b": {result: github.IssueEventsResult{}},
		}}), WithClock(func() time.Time { return fixedClock }))

		facts := decodeMaintenanceBatchFacts(t, callMaintenanceActivityBatch(t, srv, args))

		if got := facts.Repos[0]; got.Unavailable != maintenance.UnavailableFetchFailed {
			t.Errorf("acme/a = %+v, want unavailable fetch_failed", got)
		}
		if got := facts.Repos[1]; !got.Available {
			t.Errorf("acme/b = %+v, want available", got)
		}
	})
}

// TestBatchToolsStopLaunchingAfterCredentialFailure pins that a batch stops starting
// fetches once one fails on the credential. Every remaining fetch shares the token
// source, so each would fail the same way after running gh again. With one slot,
// exactly one fetch runs, whichever repository wins it.
func TestBatchToolsStopLaunchingAfterCredentialFailure(t *testing.T) {
	repos := []string{"acme/a", "acme/b", "acme/c", "acme/d"}
	clock := func() time.Time { return fixedClock }

	t.Run("authored_activity_batch", func(t *testing.T) {
		var calls atomic.Int64
		byRepo := make(map[string]authoredCanned, len(repos))
		for _, r := range repos {
			byRepo[r] = authoredCanned{err: github.ErrGHNotAuthed}
		}
		handler := authoredActivityBatchHandler(fakeFetcher{authoredCalls: &calls, authoredByRepo: byRepo}, clock, 1, authoredBatchPerRepoTimeout)

		_, _, err := handler(context.Background(), nil, authoredActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		if !errors.Is(err, github.ErrGHNotAuthed) {
			t.Fatalf("error = %v, want ErrGHNotAuthed", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("fetch count = %d, want 1 (a credential failure stops new launches)", got)
		}
	})
	t.Run("maintenance_activity_batch", func(t *testing.T) {
		var calls atomic.Int64
		byRepo := make(map[string]eventsCanned, len(repos))
		for _, r := range repos {
			byRepo[r] = eventsCanned{err: github.ErrGHNotAuthed}
		}
		handler := maintenanceActivityBatchHandler(fakeFetcher{eventsCalls: &calls, eventsByRepo: byRepo}, clock, 1, maintenanceBatchPerRepoTimeout)

		_, _, err := handler(context.Background(), nil, maintenanceActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		if !errors.Is(err, github.ErrGHNotAuthed) {
			t.Fatalf("error = %v, want ErrGHNotAuthed", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("fetch count = %d, want 1 (a credential failure stops new launches)", got)
		}
	})
}

// TestAuthoredActivityBatchCredentialFailureOutranksUnresolvableAuthor pins the order
// of the two whole-batch errors: resolving the author needs a working token, so a
// credential failure is reported even when another repository already reported the
// author unresolvable.
func TestAuthoredActivityBatchCredentialFailureOutranksUnresolvableAuthor(t *testing.T) {
	var calls atomic.Int64
	fetcher := fakeFetcher{
		authoredCalls: &calls,
		authoredSeq: func(call int64) authoredCanned {
			if call == 1 {
				return authoredCanned{err: github.ErrAuthorNotFound}
			}
			return authoredCanned{err: github.ErrGHNotAuthed}
		},
	}
	handler := authoredActivityBatchHandler(fetcher, func() time.Time { return fixedClock }, 1, authoredBatchPerRepoTimeout)

	_, _, err := handler(context.Background(), nil, authoredActivityBatchInput{Repos: []string{"acme/a", "acme/b"}, Author: "alice", Since: "2026-05-01T00:00:00Z"})

	if !errors.Is(err, github.ErrGHNotAuthed) {
		t.Fatalf("error = %v, want ErrGHNotAuthed ahead of the unresolvable author", err)
	}
}

// TestMilestoneTracksFailsTheCallOnCredentialFailure pins that milestone_tracks, whose
// only fetch otherwise degrades to an unavailable block, fails the call when that
// fetch cannot get a credential, so the caller sees the fix instead of fetch_failed.
func TestMilestoneTracksFailsTheCallOnCredentialFailure(t *testing.T) {
	for _, cf := range credentialFailures {
		t.Run(cf.name, func(t *testing.T) {
			root := writeManifestDir(t, "acme/widgets: {}\n")
			srv := New(WithFetcher(fakeFetcher{milestonesErr: cf.err}), WithManifestRoot(root), WithClock(func() time.Time { return fixedClock }))

			res := callMilestoneTracks(t, srv, map[string]any{"owner": "acme", "repo": "widgets"})

			if !res.IsError {
				t.Fatalf("IsError = false, want a call error for %v", cf.err)
			}
			if msg := contentText(res); !strings.Contains(msg, cf.want.Error()) {
				t.Errorf("error %q does not carry %q", msg, cf.want)
			}
		})
	}
}

// orderedEvents is a fakeFetcher whose ListIssueEvents outcome is chosen by call
// order rather than by repository, so a test can fix which fetch finishes first
// without depending on which repository wins a concurrency slot.
type orderedEvents struct {
	fakeFetcher
	calls *atomic.Int64
	seq   func(call int64) eventsCanned
}

func (f orderedEvents) ListIssueEvents(_ context.Context, _ string, _ time.Time, _ int) (github.IssueEventsResult, error) {
	c := f.seq(f.calls.Add(1))
	return c.result, c.err
}

// TestBatchToolsFailTheCallOnCredentialFailureBesideFinishedEntries pins that a
// credential failure fails the batch even when another repository already returned:
// the call is one error, not partial results beside it. A token revoked partway
// through a batch, with no replacement from gh, is the case this covers. One slot
// and call-ordered outcomes make the first fetch finish before the second fails, so
// the launch stop cannot turn the finished repository into a skipped one.
func TestBatchToolsFailTheCallOnCredentialFailureBesideFinishedEntries(t *testing.T) {
	repos := []string{"acme/a", "acme/b"}
	clock := func() time.Time { return fixedClock }
	assertCredentialError := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, github.ErrGHNotAuthed) {
			t.Fatalf("error = %v, want ErrGHNotAuthed beside a finished repository", err)
		}
		for _, repo := range repos {
			if strings.Contains(err.Error(), repo) {
				t.Errorf("error %q names repository %s", err, repo)
			}
		}
	}

	t.Run("authored_activity_batch", func(t *testing.T) {
		var calls atomic.Int64
		fetcher := fakeFetcher{
			authoredCalls: &calls,
			authoredSeq: func(call int64) authoredCanned {
				if call == 1 {
					return authoredCanned{result: sixCounts(1, 0, 0, 0, 0, 0)}
				}
				return authoredCanned{err: github.ErrGHNotAuthed}
			},
		}
		handler := authoredActivityBatchHandler(fetcher, clock, 1, authoredBatchPerRepoTimeout)

		_, _, err := handler(context.Background(), nil, authoredActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		assertCredentialError(t, err)
	})
	t.Run("maintenance_activity_batch", func(t *testing.T) {
		var calls atomic.Int64
		fetcher := orderedEvents{
			calls: &calls,
			seq: func(call int64) eventsCanned {
				if call == 1 {
					return eventsCanned{result: github.IssueEventsResult{}}
				}
				return eventsCanned{err: github.ErrGHNotAuthed}
			},
		}
		handler := maintenanceActivityBatchHandler(fetcher, clock, 1, maintenanceBatchPerRepoTimeout)

		_, _, err := handler(context.Background(), nil, maintenanceActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		assertCredentialError(t, err)
	})
}
