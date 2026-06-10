package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.Staleness.ThresholdDays != 30 || d.Staleness.FetchLimit != 200 {
		t.Errorf("Defaults() = %+v, want {30, 200}", d.Staleness)
	}
}

func TestResolveNoEntryFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, matched, err := NewResolver(dir, nil).Resolve("other/thing")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if matched {
		t.Error("matched = true, want false for an absent entry")
	}
	if cfg.Staleness.ThresholdDays != 30 {
		t.Errorf("ThresholdDays = %d, want 30 (default)", cfg.Staleness.ThresholdDays)
	}
}

func TestResolveMergesEntryOverDefaults(t *testing.T) {
	dir := t.TempDir()
	// Sets threshold only; fetchLimit must inherit the default.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, matched, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !matched {
		t.Error("matched = false, want true")
	}
	if cfg.Staleness.ThresholdDays != 45 {
		t.Errorf("ThresholdDays = %d, want 45 (from manifest)", cfg.Staleness.ThresholdDays)
	}
	if cfg.Staleness.FetchLimit != 200 {
		t.Errorf("FetchLimit = %d, want 200 (inherited default)", cfg.Staleness.FetchLimit)
	}
}

func TestResolveMergesDeferredLabels(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n  deferred:\n    labels: [deferred, blocked]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Deferred.Labels) != 2 || cfg.Deferred.Labels[0] != "deferred" || cfg.Deferred.Labels[1] != "blocked" {
		t.Errorf("Deferred.Labels = %v, want [deferred blocked]", cfg.Deferred.Labels)
	}
	// The deferred block must not disturb the sibling staleness merge.
	if cfg.Staleness.ThresholdDays != 45 {
		t.Errorf("ThresholdDays = %d, want 45", cfg.Staleness.ThresholdDays)
	}
}

func TestResolveStalenessOnlyLeavesDeferredEmpty(t *testing.T) {
	dir := t.TempDir()
	// An entry with no deferred block resolves to no deferred labels — the
	// reduction will report itself not-configured, which is legitimate, not an
	// error.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, matched, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !matched {
		t.Error("matched = false, want true")
	}
	if len(cfg.Deferred.Labels) != 0 {
		t.Errorf("Deferred.Labels = %v, want empty", cfg.Deferred.Labels)
	}
}

func TestResolveNoEntryLeavesDeferredEmpty(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  deferred:\n    labels: [deferred]\n  staleness:\n    thresholdDays: 45\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("other/thing")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Deferred.Labels) != 0 {
		t.Errorf("Deferred.Labels = %v, want empty (no entry, no generic default)", cfg.Deferred.Labels)
	}
}

func TestResolveMergesAreaBalance(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    labels: [http]\n    prefixes:\n      - prefix: comp\n        delimiter: \":\"\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.AreaBalance.Labels) != 1 || cfg.AreaBalance.Labels[0] != "http" {
		t.Errorf("AreaBalance.Labels = %v, want [http]", cfg.AreaBalance.Labels)
	}
	// Setting prefixes replaces the default set.
	if len(cfg.AreaBalance.Prefixes) != 1 || cfg.AreaBalance.Prefixes[0] != (PrefixRule{Prefix: "comp", Delimiter: ":"}) {
		t.Errorf("AreaBalance.Prefixes = %v, want [{comp :}]", cfg.AreaBalance.Prefixes)
	}
}

func TestResolveAreaBalanceOmittedInheritsDefaultPrefixes(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// No areaBalance block → the generic default prefixes apply (out-of-box).
	if len(cfg.AreaBalance.Prefixes) != 3 {
		t.Fatalf("AreaBalance.Prefixes = %v, want the 3 default rules", cfg.AreaBalance.Prefixes)
	}
	if cfg.AreaBalance.Prefixes[0] != (PrefixRule{Prefix: "area", Delimiter: "/"}) {
		t.Errorf("first default prefix = %v, want {area /}", cfg.AreaBalance.Prefixes[0])
	}
}

func TestResolveAreaBalanceExplicitEmptyPrefixesDisablesDefaults(t *testing.T) {
	dir := t.TempDir()
	// The omitted-vs-explicit-empty distinction: `prefixes: []` disables the
	// inherited defaults, leaving no prefix rules.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    prefixes: []\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.AreaBalance.Prefixes) != 0 {
		t.Errorf("AreaBalance.Prefixes = %v, want empty (explicit [] disables defaults)", cfg.AreaBalance.Prefixes)
	}
}

func TestResolveAreaBalanceLabelsOnlyKeepsDefaultPrefixes(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    labels: [http, fs]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.AreaBalance.Labels) != 2 {
		t.Errorf("AreaBalance.Labels = %v, want [http fs]", cfg.AreaBalance.Labels)
	}
	// Omitting prefixes keeps the inherited defaults.
	if len(cfg.AreaBalance.Prefixes) != 3 {
		t.Errorf("AreaBalance.Prefixes = %v, want the 3 inherited defaults", cfg.AreaBalance.Prefixes)
	}
}

func TestResolveRejectsEmptyAreaPrefix(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: \"  \"\n        delimiter: \"/\"\n")
	if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err == nil {
		t.Error("Resolve accepted an empty-after-trim area prefix, want error")
	}
}

func TestResolveRejectsEmptyAreaDelimiter(t *testing.T) {
	dir := t.TempDir()
	// A zero-length delimiter is a broad-match footgun (matches any label starting
	// with the prefix), so reject it.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \"\"\n")
	if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err == nil {
		t.Error("Resolve accepted an empty delimiter, want error")
	}
}

func TestResolveAcceptsWhitespaceAreaDelimiter(t *testing.T) {
	dir := t.TempDir()
	// A whitespace-containing delimiter (e.g. colon-space, as Angular uses) is a
	// legitimate separator and must not be rejected like the zero-length case.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  areaBalance:\n    prefixes:\n      - prefix: area\n        delimiter: \": \"\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve rejected a colon-space delimiter: %v", err)
	}
	if len(cfg.AreaBalance.Prefixes) != 1 || cfg.AreaBalance.Prefixes[0].Delimiter != ": " {
		t.Errorf("AreaBalance.Prefixes = %v, want one rule with delimiter %q", cfg.AreaBalance.Prefixes, ": ")
	}
}

func TestResolveCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "Acme/Widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, matched, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !matched || cfg.Staleness.ThresholdDays != 45 {
		t.Errorf("case-insensitive match failed: matched=%v threshold=%d", matched, cfg.Staleness.ThresholdDays)
	}
}

func TestResolveRejectsKeySplitAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	// A repo's entry must live in exactly one file; spread across two it is a
	// misconfiguration, not a silent last-loaded-wins.
	writeManifest(t, dir, "a-repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 10\n")
	writeManifest(t, dir, "b-repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 20\n")
	_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err == nil {
		t.Fatal("Resolve accepted a key split across files, want error")
	}
	if !strings.Contains(err.Error(), "a-repos.yml") || !strings.Contains(err.Error(), "b-repos.yml") {
		t.Errorf("error %q does not name both contributing files", err)
	}
}

func TestResolveRejectsKeySplitCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	// Case-variant keys in different files normalize to the same repo and must
	// still be rejected.
	writeManifest(t, dir, "a-repos.yml", "Acme/Widgets:\n  staleness:\n    thresholdDays: 10\n")
	writeManifest(t, dir, "b-repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 20\n")
	if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err == nil {
		t.Error("Resolve accepted a case-variant key split across files, want error")
	}
}

func TestResolveRejectsKeySplitAcrossExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	// Detection applies in OVERSTORY_MANIFESTS mode too, not just glob discovery.
	writeManifest(t, dir, "a.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 10\n")
	writeManifest(t, dir, "b.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 20\n")
	files := []string{filepath.Join(dir, "a.yml"), filepath.Join(dir, "b.yml")}
	if _, _, err := NewResolver("", files).Resolve("acme/widgets"); err == nil {
		t.Error("Resolve accepted a key split across explicit files, want error")
	}
}

func TestResolveRejectsDuplicateKeyWithinFile(t *testing.T) {
	dir := t.TempDir()
	// Two case-variant keys in one file collide on normalization; the second
	// would silently overwrite the first, so reject at parse time.
	writeManifest(t, dir, "repos.yml", "Acme/Widgets:\n  staleness:\n    thresholdDays: 10\nacme/widgets:\n  staleness:\n    thresholdDays: 20\n")
	_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err == nil {
		t.Fatal("Resolve accepted a duplicate key within one file, want error")
	}
	if !strings.Contains(err.Error(), "repos.yml") {
		t.Errorf("error %q does not name the offending file", err)
	}
}

func TestResolveYamlExtensionAlsoMatched(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yaml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	_, matched, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !matched {
		t.Error("a .yaml manifest was not discovered")
	}
}

func TestResolveToleratesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	// A future "labels" block this binary doesn't model must not break staleness.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  labels:\n    bug: critical\n  staleness:\n    thresholdDays: 45\n")
	cfg, matched, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !matched || cfg.Staleness.ThresholdDays != 45 {
		t.Errorf("unknown-field tolerance failed: matched=%v threshold=%d", matched, cfg.Staleness.ThresholdDays)
	}
}

func TestResolveRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ name, contents string }{
		{"zero threshold", "acme/widgets:\n  staleness:\n    thresholdDays: 0\n"},
		{"negative threshold", "acme/widgets:\n  staleness:\n    thresholdDays: -5\n"},
		{"zero fetchLimit", "acme/widgets:\n  staleness:\n    fetchLimit: 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.contents)
			if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err == nil {
				t.Error("Resolve accepted an invalid value, want error")
			}
		})
	}
}

func TestResolveRejectsMalformedKey(t *testing.T) {
	// Whitespace cases matter: an owner/repo carries no spaces, so a key like
	// "acme /widgets" would otherwise normalize with the space kept and never
	// match a lookup — a silent fallback to defaults.
	for _, tc := range []struct{ name, key string }{
		{"no slash", "justaname"},
		{"space before slash", "acme /widgets"},
		{"space after slash", "acme/ widgets"},
		{"internal space", "ac me/widgets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.key+":\n  staleness:\n    thresholdDays: 45\n")
			if _, _, err := NewResolver(dir, nil).Resolve(tc.key); err == nil {
				t.Error("Resolve accepted a malformed repo key, want error")
			}
		})
	}
}

func TestResolveHardFailsOnMalformedFile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets: : : not valid yaml\n")
	_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err == nil {
		t.Fatal("Resolve accepted a malformed file, want error")
	}
	if !strings.Contains(err.Error(), "repos.yml") {
		t.Errorf("error %q does not name the offending file", err)
	}
}

func TestResolveExplicitFilesMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")
	if _, _, err := NewResolver("", []string{missing}).Resolve("acme/widgets"); err == nil {
		t.Error("Resolve accepted a missing explicit file, want error")
	}
}

func TestDefaultsQuality(t *testing.T) {
	// MinBodyLength 1 keeps the universal non-empty check on out of the box; the
	// flag predicate is BodyLength < MinBodyLength, so 1 flags empty bodies.
	if d := Defaults(); d.Quality.MinBodyLength != 1 {
		t.Errorf("Quality.MinBodyLength default = %d, want 1", d.Quality.MinBodyLength)
	}
}

func TestResolveMergesQuality(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  quality:\n    minBodyLength: 30\n    requiredCategories:\n      - name: type\n        prefixes:\n          - prefix: type\n            delimiter: \"/\"\n      - name: priority\n        labels: [p0, p1]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Quality.MinBodyLength != 30 {
		t.Errorf("MinBodyLength = %d, want 30", cfg.Quality.MinBodyLength)
	}
	if len(cfg.Quality.RequiredCategories) != 2 {
		t.Fatalf("RequiredCategories = %+v, want 2", cfg.Quality.RequiredCategories)
	}
	if cfg.Quality.RequiredCategories[0].Name != "type" || len(cfg.Quality.RequiredCategories[0].Prefixes) != 1 {
		t.Errorf("category[0] = %+v, want type with one prefix", cfg.Quality.RequiredCategories[0])
	}
	if cfg.Quality.RequiredCategories[1].Name != "priority" || len(cfg.Quality.RequiredCategories[1].Labels) != 2 {
		t.Errorf("category[1] = %+v, want priority with two labels", cfg.Quality.RequiredCategories[1])
	}
}

func TestResolveNoEntryUsesQualityDefault(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	// An unmatched repo still resolves+validates with the default MinBodyLength 1 —
	// the guard against a <= 0 validator that would reject every unconfigured repo.
	cfg, matched, err := NewResolver(dir, nil).Resolve("other/thing")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if matched {
		t.Error("matched = true, want false")
	}
	if cfg.Quality.MinBodyLength != 1 {
		t.Errorf("MinBodyLength = %d, want 1 (default)", cfg.Quality.MinBodyLength)
	}
	if len(cfg.Quality.RequiredCategories) != 0 {
		t.Errorf("RequiredCategories = %v, want empty (no default)", cfg.Quality.RequiredCategories)
	}
}

func TestResolveAcceptsZeroMinBodyLength(t *testing.T) {
	dir := t.TempDir()
	// 0 is the explicit disable value, not an invalid one.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  quality:\n    minBodyLength: 0\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve rejected minBodyLength 0 (disable value): %v", err)
	}
	if cfg.Quality.MinBodyLength != 0 {
		t.Errorf("MinBodyLength = %d, want 0", cfg.Quality.MinBodyLength)
	}
}

func TestResolveQualityEmptyCategoriesNotConfigured(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  quality:\n    requiredCategories: []\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Quality.RequiredCategories) != 0 {
		t.Errorf("RequiredCategories = %v, want empty (explicit [] is not-configured, not an error)", cfg.Quality.RequiredCategories)
	}
	if cfg.Quality.MinBodyLength != 1 {
		t.Errorf("MinBodyLength = %d, want 1 (inherited default)", cfg.Quality.MinBodyLength)
	}
}

func TestResolveRejectsInvalidQuality(t *testing.T) {
	for _, tc := range []struct{ name, contents string }{
		{"negative minBodyLength", "acme/widgets:\n  quality:\n    minBodyLength: -1\n"},
		{"empty category name", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: \"  \"\n        labels: [x]\n"},
		{"category without matchers", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: type\n"},
		{"duplicate category name", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: type\n        labels: [a]\n      - name: Type\n        labels: [b]\n"},
		{"empty category prefix", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: type\n        prefixes:\n          - prefix: \"  \"\n            delimiter: \"/\"\n"},
		{"empty category delimiter", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: type\n        prefixes:\n          - prefix: type\n            delimiter: \"\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.contents)
			if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err == nil {
				t.Error("Resolve accepted an invalid quality config, want error")
			}
		})
	}
}

func TestResolveQualityCategoryErrorNamesCategory(t *testing.T) {
	dir := t.TempDir()
	// The shared prefix validator must carry the category name into the message so
	// it stays actionable (the MAJOR-4 field-path requirement).
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: priority\n        prefixes:\n          - prefix: priority\n            delimiter: \"\"\n")
	_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err == nil {
		t.Fatal("want error for empty delimiter")
	}
	if !strings.Contains(err.Error(), "priority") {
		t.Errorf("error %q does not name the offending category", err)
	}
}
