// Package manifest resolves a repository's conventions from per-repo manifest
// files deep-merged over generic defaults. Manifests are discovered from an XDG
// drop-in directory (or an explicit file list), keyed by "owner/repo", so a
// single server serves any repository without code changes. It resolves the
// conventions each backlog reduction consumes (thresholds, label taxonomies, and
// similar), each as its own config block.
package manifest

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/goccy/go-yaml"
)

// Config is the resolved convention set for one repository.
type Config struct {
	Staleness   StalenessConfig
	Deferred    DeferredConfig
	AreaBalance AreaBalanceConfig
	Quality     QualityConfig
	Overlap     OverlapConfig
}

// StalenessConfig holds resolved staleness conventions. ThresholdDays is the
// inactivity threshold (an issue is stale at or beyond it); FetchLimit caps how
// many open issues are fetched to compute the reduction.
type StalenessConfig struct {
	ThresholdDays int
	FetchLimit    int
}

// DeferredConfig holds the resolved deferred-review convention: the maintainer-
// declared labels that mark an open issue as parked. There is no generic default
// — "deferred" is repo-specific — so a repository that declares none leaves
// Labels empty and the deferred reduction reports itself not-configured.
type DeferredConfig struct {
	Labels []string
}

// AreaBalanceConfig holds the resolved area-balance convention: how issues are
// classified into functional areas. An area is identified by an explicit label
// (Labels) and/or a prefix rule (Prefixes), unioned. Unlike Deferred, this has a
// generic default (common `area/`-style prefixes) so the reduction classifies
// typical repos out of the box.
type AreaBalanceConfig struct {
	Labels   []string
	Prefixes []PrefixRule
}

// PrefixRule identifies an area by a label prefix and delimiter (e.g. prefix
// "area", delimiter "/"): a label matches when it starts with prefix+delimiter,
// and the area name is the remainder. The delimiter is configurable because
// real-world conventions are fragmented (`area/`, `area:`, `area-`).
type PrefixRule struct {
	Prefix    string `yaml:"prefix"`
	Delimiter string `yaml:"delimiter"`
}

// QualityConfig holds the resolved issue-quality convention. MinBodyLength is the
// minimum trimmed body length an open issue must have before its body reads as
// substantive (1, the default, means "non-empty"; a higher value flags thin
// bodies; 0 disables the body check). RequiredCategories declares label families
// every issue is expected to carry one of — the configurable, per-category
// label-coverage signal, repo-specific like Deferred (no generic default).
type QualityConfig struct {
	MinBodyLength      int
	RequiredCategories []CategoryRule
}

// CategoryRule is one required label family: an issue satisfies it by carrying at
// least one label that matches the family by explicit Labels and/or Prefixes
// (the same label-or-prefix union AreaBalanceConfig uses). Name is the family's
// display name, echoed in the reduction's per-category counts.
type CategoryRule struct {
	Name     string
	Labels   []string
	Prefixes []PrefixRule
}

// OverlapConfig holds the resolved title-overlap convention. TitleSimilarityThreshold
// is the char-trigram Sørensen–Dice score two open-issue titles must reach to be
// linked as candidate duplicates: 0 disables the reduction, 1 requires an exact
// (normalized) match, and the default (0.5) groups clearly-similar titles. Unlike
// Deferred, it has a generic default so the reduction works on any repo out of
// the box — title similarity needs no repo-specific vocabulary.
type OverlapConfig struct {
	TitleSimilarityThreshold float64
}

// Defaults returns the generic defaults applied when a repository has no
// manifest entry, or for fields its entry omits. These are the one place a
// convention value is allowed to live in Go — the fallback, not a repo's
// declared convention. The area-balance prefixes cover the most common
// area-label conventions so an unconfigured repo still classifies out of the box.
func Defaults() Config {
	return Config{
		Staleness: StalenessConfig{ThresholdDays: 30, FetchLimit: 200},
		AreaBalance: AreaBalanceConfig{Prefixes: []PrefixRule{
			{Prefix: "area", Delimiter: "/"},
			{Prefix: "area", Delimiter: ":"},
			{Prefix: "area", Delimiter: "-"},
		}},
		// MinBodyLength 1 keeps the universal "body must be non-empty" check on out
		// of the box; RequiredCategories has no default — label families are
		// repo-specific, like Deferred.
		Quality: QualityConfig{MinBodyLength: 1},
		// 0.5 links clearly-similar titles while leaving paraphrases that share only
		// a word or two below the bar; tunable per repo.
		Overlap: OverlapConfig{TitleSimilarityThreshold: 0.5},
	}
}

// Resolver resolves merged Configs from on-disk manifests. root is the
// drop-in directory to glob; files, when non-empty, is an explicit ordered
// file list that overrides the directory. Both are injected so tests isolate
// discovery from the real config home.
type Resolver struct {
	root  string
	files []string
}

// NewResolver builds a Resolver over the given drop-in directory and optional
// explicit file list.
func NewResolver(root string, files []string) *Resolver {
	return &Resolver{root: root, files: files}
}

// Resolve returns the merged Config for ownerRepo, generic defaults deep-merged
// under the matching manifest entry (matched reports whether one was found).
// Matching is case-insensitive. A manifest that is unreadable, unparseable, or
// declares an invalid value is a hard error naming the offending file — the
// caller is responsible for keeping that detail off any caller-facing channel.
func (r *Resolver) Resolve(ownerRepo string) (Config, bool, error) {
	paths, err := r.discover()
	if err != nil {
		return Config{}, false, err
	}

	key := strings.ToLower(strings.TrimSpace(ownerRepo))
	var winning *fileConfig    // the single matching entry, merged over defaults
	var winningFile string     // its source path, for naming validation errors
	var carryingFiles []string // every file declaring the key, to catch a split entry
	for _, p := range paths {
		entries, lerr := loadFile(p)
		if lerr != nil {
			return Config{}, false, lerr
		}
		if fc, ok := entries[key]; ok {
			entry := fc
			winning = &entry
			winningFile = p
			carryingFiles = append(carryingFiles, p)
		}
	}
	// A repo's entry must live in exactly one file. Spread across several, the old
	// behavior silently kept only the last; fail loud instead so the operator can
	// consolidate rather than lose a convention invisibly.
	if len(carryingFiles) > 1 {
		return Config{}, false, fmt.Errorf(
			"manifest key %q is defined in multiple files (%s); define it in exactly one",
			ownerRepo, strings.Join(carryingFiles, ", "))
	}

	merged := Defaults()
	matched := winning != nil
	if matched {
		merged = mergeConfig(merged, *winning)
	}
	if verr := validate(merged, ownerRepo, winningFile); verr != nil {
		return Config{}, false, verr
	}
	return merged, matched, nil
}

func (r *Resolver) discover() ([]string, error) {
	if len(r.files) > 0 {
		for _, p := range r.files {
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("manifest file %q from OVERSTORY_MANIFESTS: %w", p, err)
			}
		}
		return r.files, nil
	}
	if r.root == "" {
		return nil, nil
	}
	var paths []string
	for _, ext := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(r.root, ext))
		if err != nil {
			return nil, fmt.Errorf("globbing manifests in %q: %w", r.root, err)
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths) // deterministic order so a duplicate-key error names files stably
	return paths, nil
}

// fileConfig and stalenessFile decode a manifest entry. Pointer fields
// distinguish an omitted field from an explicit zero, so merge only overrides
// what the manifest actually set. Unknown fields are tolerated (goccy is
// lenient by default), so a manifest also carrying config for reductions this
// binary doesn't yet implement still resolves for staleness.
type fileConfig struct {
	Staleness   *stalenessFile   `yaml:"staleness"`
	Deferred    *deferredFile    `yaml:"deferred"`
	AreaBalance *areaBalanceFile `yaml:"areaBalance"`
	Quality     *qualityFile     `yaml:"quality"`
	Overlap     *overlapFile     `yaml:"overlap"`
}

type stalenessFile struct {
	ThresholdDays *int `yaml:"thresholdDays"`
	FetchLimit    *int `yaml:"fetchLimit"`
}

// deferredFile decodes the deferred block. A pointer-to-slice distinguishes an
// omitted labels list from an explicit empty one, keeping the merge idiom
// uniform — though both resolve to a not-configured reduction.
type deferredFile struct {
	Labels *[]string `yaml:"labels"`
}

// areaBalanceFile decodes the areaBalance block. Each list is a pointer-to-slice
// so an omitted list inherits the default (notably the default prefixes) while an
// explicit empty list (`prefixes: []`) disables it — the omitted-vs-empty
// distinction the pointer idiom exists for.
type areaBalanceFile struct {
	Labels   *[]string     `yaml:"labels"`
	Prefixes *[]PrefixRule `yaml:"prefixes"`
}

// qualityFile decodes the quality block. The outer pointers give the omitted-vs-
// present distinction; RequiredCategories merges as a whole-list replace (like
// deferred.Labels, not areaBalance's field-by-field merge) since categories are a
// list, not a fixed field set. Category elements are value-typed — the pointer
// idiom doesn't extend into slice elements, and doesn't need to here.
type qualityFile struct {
	MinBodyLength      *int            `yaml:"minBodyLength"`
	RequiredCategories *[]categoryFile `yaml:"requiredCategories"`
}

type categoryFile struct {
	Name     string       `yaml:"name"`
	Labels   []string     `yaml:"labels"`
	Prefixes []PrefixRule `yaml:"prefixes"`
}

// overlapFile decodes the overlap block. The pointer distinguishes an omitted
// threshold (inherit the 0.5 default) from an explicit 0 (disable the reduction),
// the same omitted-vs-explicit distinction the pointer idiom exists for.
type overlapFile struct {
	TitleSimilarityThreshold *float64 `yaml:"titleSimilarityThreshold"`
}

func loadFile(path string) (map[string]fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %q: %w", path, err)
	}
	var entries map[string]fileConfig
	if uerr := yaml.Unmarshal(data, &entries); uerr != nil {
		return nil, fmt.Errorf("parsing manifest %q: %w", path, uerr)
	}
	normalized := make(map[string]fileConfig, len(entries))
	for k, v := range entries {
		trimmed := strings.TrimSpace(k)
		parts := strings.Split(trimmed, "/")
		// owner/repo carries no internal whitespace; a key like "acme /widgets"
		// would otherwise normalize with the space kept and never match a lookup,
		// silently falling back to defaults.
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("manifest %q: malformed repo key %q (want \"owner/repo\")", path, k)
		}
		// Keys are case-insensitive, so two case-variant keys collide here; the
		// second would silently overwrite the first. Reject rather than drop one.
		lk := strings.ToLower(trimmed)
		if _, dup := normalized[lk]; dup {
			return nil, fmt.Errorf("manifest %q: key %q is defined more than once (keys are case-insensitive)", path, k)
		}
		normalized[lk] = v
	}
	return normalized, nil
}

func mergeConfig(base Config, o fileConfig) Config {
	if o.Staleness != nil {
		if o.Staleness.ThresholdDays != nil {
			base.Staleness.ThresholdDays = *o.Staleness.ThresholdDays
		}
		if o.Staleness.FetchLimit != nil {
			base.Staleness.FetchLimit = *o.Staleness.FetchLimit
		}
	}
	if o.Deferred != nil && o.Deferred.Labels != nil {
		base.Deferred.Labels = *o.Deferred.Labels
	}
	if o.AreaBalance != nil {
		// Field-level: omitting prefixes inherits the default prefixes; an explicit
		// empty list disables them.
		if o.AreaBalance.Labels != nil {
			base.AreaBalance.Labels = *o.AreaBalance.Labels
		}
		if o.AreaBalance.Prefixes != nil {
			base.AreaBalance.Prefixes = *o.AreaBalance.Prefixes
		}
	}
	if o.Quality != nil {
		if o.Quality.MinBodyLength != nil {
			base.Quality.MinBodyLength = *o.Quality.MinBodyLength
		}
		// Whole-list replace: a list keyed by nothing can't be field-merged, and the
		// universal body/no-label checks still cover a repo that declares none.
		if o.Quality.RequiredCategories != nil {
			cats := make([]CategoryRule, 0, len(*o.Quality.RequiredCategories))
			for _, c := range *o.Quality.RequiredCategories {
				// Trim the name at the resolution boundary so the cleaned value is what
				// flows into the reduction's display name, count keys, and error paths —
				// a name like "type " would otherwise validate but key output oddly.
				c.Name = strings.TrimSpace(c.Name)
				cats = append(cats, CategoryRule(c))
			}
			base.Quality.RequiredCategories = cats
		}
	}
	if o.Overlap != nil && o.Overlap.TitleSimilarityThreshold != nil {
		base.Overlap.TitleSimilarityThreshold = *o.Overlap.TitleSimilarityThreshold
	}
	return base
}

func validate(c Config, ownerRepo, file string) error {
	if c.Staleness.ThresholdDays <= 0 {
		return fmt.Errorf("manifest %q for %q: staleness.thresholdDays must be > 0, got %d", file, ownerRepo, c.Staleness.ThresholdDays)
	}
	if c.Staleness.FetchLimit <= 0 {
		return fmt.Errorf("manifest %q for %q: staleness.fetchLimit must be > 0, got %d", file, ownerRepo, c.Staleness.FetchLimit)
	}
	if err := validatePrefixes(c.AreaBalance.Prefixes, "areaBalance.prefixes", ownerRepo, file); err != nil {
		return err
	}
	// 0 disables the body check; only a negative value is meaningless. Rejecting
	// <= 0 here would reject the disable value and the unconfigured default.
	if c.Quality.MinBodyLength < 0 {
		return fmt.Errorf("manifest %q for %q: quality.minBodyLength must be >= 0, got %d", file, ownerRepo, c.Quality.MinBodyLength)
	}
	seen := make(map[string]struct{}, len(c.Quality.RequiredCategories))
	for _, cat := range c.Quality.RequiredCategories {
		name := strings.TrimSpace(cat.Name)
		if name == "" {
			return fmt.Errorf("manifest %q for %q: quality.requiredCategories has a rule with an empty name", file, ownerRepo)
		}
		// Names key the per-category counts case-insensitively; two that collide
		// would miscount, so reject rather than silently merge them.
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("manifest %q for %q: quality.requiredCategories has a duplicate category name %q", file, ownerRepo, cat.Name)
		}
		seen[key] = struct{}{}
		if len(cat.Labels) == 0 && len(cat.Prefixes) == 0 {
			return fmt.Errorf("manifest %q for %q: quality.requiredCategories category %q has no labels or prefixes", file, ownerRepo, cat.Name)
		}
		if err := validatePrefixes(cat.Prefixes, fmt.Sprintf("quality.requiredCategories[%s].prefixes", name), ownerRepo, file); err != nil {
			return err
		}
	}
	// A Sørensen–Dice score is in [0,1]; 0 disables the reduction and 1 is exact-
	// match-only, so both bounds are valid — only a value outside the range is
	// meaningless. (Mirror quality.minBodyLength's inclusive care, not staleness's
	// <= 0 rejection, which would wrongly reject the disable value.) NaN is rejected
	// explicitly: every comparison against NaN is false, so a YAML `.nan` would pass
	// the range check and then silently make only exact-match titles link.
	if math.IsNaN(c.Overlap.TitleSimilarityThreshold) || c.Overlap.TitleSimilarityThreshold < 0 || c.Overlap.TitleSimilarityThreshold > 1 {
		return fmt.Errorf("manifest %q for %q: overlap.titleSimilarityThreshold must be in [0,1], got %g", file, ownerRepo, c.Overlap.TitleSimilarityThreshold)
	}
	return nil
}

// validatePrefixes rejects the two prefix-rule footguns shared by area balance and
// quality categories. fieldPath names the offending field in the error (e.g.
// "areaBalance.prefixes") so the message stays specific and actionable.
func validatePrefixes(prefixes []PrefixRule, fieldPath, ownerRepo, file string) error {
	for _, p := range prefixes {
		// An empty prefix would match every label; reject so misconfiguration fails
		// loud rather than silently classifying everything into one bucket.
		if strings.TrimSpace(p.Prefix) == "" {
			return fmt.Errorf("manifest %q for %q: %s has a rule with an empty prefix", file, ownerRepo, fieldPath)
		}
		// A zero-length delimiter makes the rule match any label starting with the
		// prefix, with the real separator leaking into the projected name — a
		// broad-match footgun. The check is exact (not trim-based) because a
		// whitespace delimiter like ": " is a legitimate separator, unlike a
		// whitespace prefix.
		if p.Delimiter == "" {
			return fmt.Errorf("manifest %q for %q: %s rule %q has an empty delimiter", file, ownerRepo, fieldPath, p.Prefix)
		}
	}
	return nil
}
