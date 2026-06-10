// Package manifest resolves a repository's conventions from per-repo manifest
// files deep-merged over generic defaults. Manifests are discovered from an XDG
// drop-in directory (or an explicit file list), keyed by "owner/repo", so a
// single server serves any repository without code changes. This slice models
// only staleness conventions.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Config is the resolved convention set for one repository.
type Config struct {
	Staleness StalenessConfig
}

// StalenessConfig holds resolved staleness conventions. ThresholdDays is the
// inactivity threshold (an issue is stale at or beyond it); FetchLimit caps how
// many open issues are fetched to compute the reduction.
type StalenessConfig struct {
	ThresholdDays int
	FetchLimit    int
}

// Defaults returns the generic defaults applied when a repository has no
// manifest entry, or for fields its entry omits. These are the one place a
// convention value is allowed to live in Go — the fallback, not a repo's
// declared convention.
func Defaults() Config {
	return Config{Staleness: StalenessConfig{ThresholdDays: 30, FetchLimit: 200}}
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
	Staleness *stalenessFile `yaml:"staleness"`
}

type stalenessFile struct {
	ThresholdDays *int `yaml:"thresholdDays"`
	FetchLimit    *int `yaml:"fetchLimit"`
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
		parts := strings.Split(strings.TrimSpace(k), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("manifest %q: malformed repo key %q (want \"owner/repo\")", path, k)
		}
		// Keys are case-insensitive, so two case-variant keys collide here; the
		// second would silently overwrite the first. Reject rather than drop one.
		lk := strings.ToLower(strings.TrimSpace(k))
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
	return base
}

func validate(c Config, ownerRepo, file string) error {
	if c.Staleness.ThresholdDays <= 0 {
		return fmt.Errorf("manifest %q for %q: staleness.thresholdDays must be > 0, got %d", file, ownerRepo, c.Staleness.ThresholdDays)
	}
	if c.Staleness.FetchLimit <= 0 {
		return fmt.Errorf("manifest %q for %q: staleness.fetchLimit must be > 0, got %d", file, ownerRepo, c.Staleness.FetchLimit)
	}
	return nil
}
