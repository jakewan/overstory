package server

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakewan/overstory/internal/github"
)

// captureLog redirects the standard logger, which the fan-outs' stderr diagnostics
// write to, into a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// TestBatchToolsLogNothingForFetchesTheyCancel pins that a fetch a batch cancelled
// itself writes no stderr diagnostic. The batch fails with the failure that caused the
// cancellation and discards every entry, so a fetch-failed line for the cancelled
// fetch would report a failure that never happened. The first fetch blocks until its
// context is done; the second meets a failure every repository shares.
func TestBatchToolsLogNothingForFetchesTheyCancel(t *testing.T) {
	const perRepoTimeout = 10 * time.Second
	repos := []string{"acme/a", "acme/b"}
	clock := func() time.Time { return fixedClock }

	t.Run("authored_activity_batch", func(t *testing.T) {
		logged := captureLog(t)
		var calls atomic.Int64
		fetcher := fakeFetcher{
			authoredCalls: &calls,
			authoredSeq: func(call int64) authoredCanned {
				if call == 1 {
					return authoredCanned{block: true}
				}
				return authoredCanned{err: github.ErrAuthorNotFound}
			},
		}

		_, _, err := authoredActivityBatchHandler(fetcher, clock, 2, perRepoTimeout)(context.Background(), nil, authoredActivityBatchInput{Repos: repos, Author: "ghost", Since: "2026-05-01T00:00:00Z"})

		if err == nil {
			t.Fatal("err = nil, want the unresolvable-author error")
		}
		if got := logged.String(); strings.Contains(got, "authored activity fetch") {
			t.Errorf("logged %q for a fetch the batch cancelled", got)
		}
	})
	t.Run("maintenance_activity_batch", func(t *testing.T) {
		logged := captureLog(t)
		var calls atomic.Int64
		fetcher := orderedEvents{
			calls: &calls,
			seq: func(call int64) eventsCanned {
				if call == 1 {
					return eventsCanned{block: true}
				}
				return eventsCanned{err: github.ErrGHNotAuthed}
			},
		}

		_, _, err := maintenanceActivityBatchHandler(fetcher, clock, 2, perRepoTimeout)(context.Background(), nil, maintenanceActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		if err == nil {
			t.Fatal("err = nil, want the credential error")
		}
		if got := logged.String(); strings.Contains(got, "maintenance activity fetch") {
			t.Errorf("logged %q for a fetch the batch cancelled", got)
		}
	})
}

// TestBatchToolsLogRepositoryDeadline pins the other side: a repository whose own
// deadline trips degrades to fetch_failed in a result the caller receives, so its
// cause still reaches stderr.
func TestBatchToolsLogRepositoryDeadline(t *testing.T) {
	const perRepoTimeout = 50 * time.Millisecond
	repos := []string{"acme/slow", "acme/fast"}
	clock := func() time.Time { return fixedClock }

	t.Run("authored_activity_batch", func(t *testing.T) {
		logged := captureLog(t)
		fetcher := authoredBatchFetcher(map[string]authoredCanned{
			"acme/slow": {block: true},
			"acme/fast": {result: sixCounts(1, 0, 0, 0, 0, 0)},
		})

		_, _, err := authoredActivityBatchHandler(fetcher, clock, 2, perRepoTimeout)(context.Background(), nil, authoredActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := logged.String(); !strings.Contains(got, "authored activity fetch for acme/slow") {
			t.Errorf("logged %q, want the slow repository's deadline", got)
		}
	})
	t.Run("maintenance_activity_batch", func(t *testing.T) {
		logged := captureLog(t)
		fetcher := fakeFetcher{eventsByRepo: map[string]eventsCanned{
			"acme/slow": {block: true},
			"acme/fast": {result: github.IssueEventsResult{}},
		}}

		_, _, err := maintenanceActivityBatchHandler(fetcher, clock, 2, perRepoTimeout)(context.Background(), nil, maintenanceActivityBatchInput{Repos: repos, Author: "alice", Since: "2026-05-01T00:00:00Z"})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := logged.String(); !strings.Contains(got, "maintenance activity fetch for acme/slow") {
			t.Errorf("logged %q, want the slow repository's deadline", got)
		}
	})
}
