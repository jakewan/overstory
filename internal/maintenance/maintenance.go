// Package maintenance is the per-repo maintenance-activity reduction: given one
// repository, one actor login, and a bounded time window, it carries the
// state-mutation attention that user paid to existing issues and pull requests —
// the relabeling, milestoning, deferral-labeling, closing/reopening, assigning,
// and renaming that authored-activity counts structurally miss (a grooming
// afternoon produces near-zero authored counts). It groups those mutations by the
// item they touched, newest-touched first, so a resumption consumer can answer
// "what did I just change, and when" precisely.
//
// It is manifest-blind and a pure reduction: the github layer fetches the
// repository's REST issue-events stream, and this package filters it to the
// measured actor, the mutation event subset, and the window, then groups. The
// server reduces; the caller renders — deciding how to weight, narrate, and split
// the issue/PR mix is the caller's job.
package maintenance

import (
	"sort"
	"strings"
	"time"

	"github.com/jakewan/overstory/internal/github"
	"github.com/jakewan/overstory/internal/reduce"
)

// mutationEvents is the state-mutation event subset the reduction keeps: the
// maintenance attention a measured actor pays to existing items. Every other
// event in the stream (subscribed, mentioned, referenced, merged, the
// head-ref/copilot churn, …) is dropped — it is not a deliberate grooming
// mutation. The set is fixed here, not manifest-supplied, because it names
// GitHub's own event vocabulary, not a per-repo convention.
var mutationEvents = map[string]struct{}{
	"labeled":      {},
	"unlabeled":    {},
	"milestoned":   {},
	"demilestoned": {},
	"closed":       {},
	"reopened":     {},
	"assigned":     {},
	"renamed":      {},
}

// Facts is the maintenance-activity reduction's output: review-level identity
// (the repo, the actor, the window, the generation time) plus the per-item
// activity list. Repo and GeneratedAt describe the whole read and are stamped by
// the server handler, mirroring the other tools; the reduction fills the actor,
// window, and items. Items is never nil (it serializes as [] rather than null).
// Truncated is the window-coverage fidelity signal: true when the fetch could not
// prove it scanned back past the window floor, so a recent mutation may be
// missing. RateLimit is the fetch's REST core-pool budget, omitted when none was
// observed (essentially never, since the REST endpoint always returns headers).
type Facts struct {
	Repo        string                 `json:"repo"`
	Author      string                 `json:"author"`
	Since       time.Time              `json:"since"`
	Until       time.Time              `json:"until"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Items       []ItemActivity         `json:"items"`
	Truncated   bool                   `json:"truncated"`
	RateLimit   *reduce.RateLimitFacts `json:"rateLimit,omitempty"`
}

// ItemActivity is one issue or pull request the actor touched in the window, with
// the qualifying mutations grouped under it. IsPullRequest lets the caller split
// the issue/PR mix (the stream is roughly a third PR events); the reduction
// surfaces the flag and stays tag-blind rather than filtering. Events are ordered
// oldest-first within the item (chronological), so a reader follows the sequence
// of changes in the order they happened.
type ItemActivity struct {
	Number        int     `json:"number"`
	Title         string  `json:"title"`
	IsPullRequest bool    `json:"isPullRequest"`
	Events        []Event `json:"events"`
}

// Event is one state mutation the actor performed: its type and instant, whether
// GitHub attributed it to an app (so the caller can exclude automation-driven
// churn), and the per-type payload — a label name, a milestone title, an assignee
// login, or a rename's before/after. The payload fields carry omitempty so a
// closed/reopened event (which carries no payload) serializes lean.
type Event struct {
	Type          string    `json:"type"`
	At            time.Time `json:"at"`
	ViaAutomation bool      `json:"viaAutomation"`
	Label         string    `json:"label,omitempty"`
	Milestone     string    `json:"milestone,omitempty"`
	Assignee      string    `json:"assignee,omitempty"`
	RenameFrom    string    `json:"renameFrom,omitempty"`
	RenameTo      string    `json:"renameTo,omitempty"`
}

// Reduce assembles the maintenance facts from a fetched events result, filtering
// to the actor's mutations in the window and grouping them by item. It is pure:
// Repo, GeneratedAt, and RateLimit are stamped by the caller; the window is
// echoed normalized to UTC so the bounds match the instants the filter used.
func Reduce(result github.IssueEventsResult, author string, since, until time.Time) Facts {
	return Facts{
		Author:    author,
		Since:     since.UTC(),
		Until:     until.UTC(),
		Items:     itemsFrom(result.Events, author, since, until),
		Truncated: result.Truncated,
	}
}

// itemsFrom is the shared grouping the single-repo Reduce and the per-repo batch
// entry both use, so the two surfaces can never diverge on what an item or its
// event ordering means. It filters the raw stream to the actor's in-window
// mutations (actor matched case-insensitively, since GitHub logins are
// case-folding), groups by item number, orders each item's events chronologically
// by GitHub's monotonic event id, and orders the items by most-recently-touched
// first — the order a resumption consumer wants. The returned slice is never nil
// so it serializes as [].
func itemsFrom(events []github.IssueEvent, author string, since, until time.Time) []ItemActivity {
	// grouped collects one accumulator per touched item; latestID tracks each
	// item's newest qualifying event so items can be ordered most-recent-first.
	type group struct {
		item     ItemActivity
		raw      []github.IssueEvent
		latestID int64
	}
	grouped := make(map[int]*group)
	for _, e := range events {
		if !strings.EqualFold(e.Actor, author) {
			continue
		}
		if _, ok := mutationEvents[e.Type]; !ok {
			continue
		}
		// since <= at <= until: both bounds inclusive, matching the window the caller
		// echoes. An event exactly on a bound is in-window.
		if e.CreatedAt.Before(since) || e.CreatedAt.After(until) {
			continue
		}
		g := grouped[e.IssueNumber]
		if g == nil {
			g = &group{item: ItemActivity{Number: e.IssueNumber, Title: e.IssueTitle, IsPullRequest: e.IssueIsPR}}
			grouped[e.IssueNumber] = g
		}
		g.raw = append(g.raw, e)
		if e.EventID > g.latestID {
			g.latestID = e.EventID
		}
	}

	items := make([]ItemActivity, 0, len(grouped))
	for _, g := range grouped {
		// Chronological within the item: the monotonic event id is a more precise
		// clock than created_at (two mutations can share a timestamp to the second),
		// so order by id ascending.
		sort.Slice(g.raw, func(i, j int) bool { return g.raw[i].EventID < g.raw[j].EventID })
		g.item.Events = make([]Event, 0, len(g.raw))
		for _, e := range g.raw {
			g.item.Events = append(g.item.Events, toEvent(e))
		}
		items = append(items, g.item)
	}
	// Most-recently-touched first, ties broken by item number descending for a
	// deterministic order independent of the input stream and map iteration.
	sort.Slice(items, func(i, j int) bool {
		li, lj := grouped[items[i].Number].latestID, grouped[items[j].Number].latestID
		if li != lj {
			return li > lj
		}
		return items[i].Number > items[j].Number
	})
	return items
}

// toEvent projects a fetched event to the output event, carrying only the payload
// fields its type populates (the rest stay empty and omit). EventID is dropped —
// it is a fetch-layer dedup and ordering key, not part of the caller contract.
func toEvent(e github.IssueEvent) Event {
	return Event{
		Type:          e.Type,
		At:            e.CreatedAt.UTC(),
		ViaAutomation: e.ViaAutomation,
		Label:         e.Label,
		Milestone:     e.Milestone,
		Assignee:      e.Assignee,
		RenameFrom:    e.RenameFrom,
		RenameTo:      e.RenameTo,
	}
}
