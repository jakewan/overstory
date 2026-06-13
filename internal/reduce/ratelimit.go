package reduce

import "time"

// RateLimitFacts is the GraphQL points-budget snapshot from a fetch, so a caller
// can pace itself: the points Remaining in the current window and the ResetAt
// instant it refills. It is shared across reductions because every fetch carries
// the same budget shape; a reduction's Facts root embeds it as a pointer and
// omits it (nil) when the fetch carried no budget block, so a caller never
// mistakes an unknown budget for a present one.
type RateLimitFacts struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}
