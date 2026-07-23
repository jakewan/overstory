// Package summary holds overstory's session-orientation reduction: pure
// functions that turn a repository's fetched issues, milestones, and pull
// requests into the compact structured facts a caller needs to answer "given
// what's open now, what should I pick up?". It is the orientation counterpart to
// the backlog package's grooming reduction — a distinct stance over shared
// inputs — and like it depends only on the fetched shapes it reduces, never on
// MCP or transport types, so every reduction is deterministic and testable.
package summary

import (
	"time"

	"github.com/jakewan/overstory/internal/criticalpath"
	"github.com/jakewan/overstory/internal/dependency"
	"github.com/jakewan/overstory/internal/reduce"
)

// Facts is the full project-summary reduction: orientation-level identity plus
// one block per orientation signal. Repo and GeneratedAt describe the whole
// summary; each block carries its own counts and truncation seams so a caller
// renders them independently. Milestones and OpenPRs each need their own fetch,
// so each can degrade to an unavailable block (see their Available fields)
// without failing the whole summary.
type Facts struct {
	Repo        string    `json:"repo"`
	GeneratedAt time.Time `json:"generatedAt"`
	// The orientation-signal blocks are pointers with omitempty so block projection
	// can omit an unrequested one entirely. backlog.Facts carries the full rationale
	// for the shape; it is not repeated here, where a copy would drift from it.
	Milestones      *MilestoneFacts            `json:"milestones,omitempty"`
	AreaInventory   *AreaInventoryFacts        `json:"areaInventory,omitempty"`
	Hygiene         *HygieneFacts              `json:"hygiene,omitempty"`
	OpenPRs         *PullRequestFacts          `json:"openPRs,omitempty"`
	Recommendations *RecommendationFacts       `json:"recommendations,omitempty"`
	CriticalPath    *criticalpath.Facts        `json:"criticalPath,omitempty"`
	Dependencies    *dependency.Classification `json:"dependencies,omitempty"`
	OpenIssueSet    reduce.OpenIssueSetFacts   `json:"openIssueSet"`
	RateLimit       *reduce.RateLimitFacts     `json:"rateLimit,omitempty"`
	// SizeBound is set only when the response had to be trimmed to fit the
	// configured byte budget; absent (nil) on a response that fit untouched.
	SizeBound *reduce.SizeBoundFacts `json:"sizeBound,omitempty"`
}
