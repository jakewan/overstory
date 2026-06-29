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

func TestDefaultsResponseMaxBytes(t *testing.T) {
	if d := Defaults(); d.Response.MaxBytes != 20000 {
		t.Errorf("Defaults().Response.MaxBytes = %d, want 20000", d.Response.MaxBytes)
	}
}

func TestResolveMergesResponseMaxBytes(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  response:\n    maxBytes: 50000\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Response.MaxBytes != 50000 {
		t.Errorf("Response.MaxBytes = %d, want 50000 (from manifest)", cfg.Response.MaxBytes)
	}
}

func TestResolveResponseMaxBytesOmittedInheritsDefault(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 45\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Response.MaxBytes != 20000 {
		t.Errorf("Response.MaxBytes = %d, want 20000 (inherited default)", cfg.Response.MaxBytes)
	}
}

func TestResolveRejectsResponseMaxBytesBelowFloor(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  response:\n    maxBytes: 100\n")
	_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err == nil {
		t.Fatal("Resolve err = nil, want a below-floor rejection")
	}
	if !strings.Contains(err.Error(), "response.maxBytes") {
		t.Errorf("error = %q, want it to name response.maxBytes", err)
	}
}

// TestResolveResponseMaxBytesFloorBoundary pins the floor as inclusive: the floor
// value itself is accepted and one below is rejected, so a future `<=`-for-`<` slip
// in validate (which would reject the documented floor) fails here.
func TestResolveResponseMaxBytesFloorBoundary(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/atfloor:\n  response:\n    maxBytes: 4096\nacme/belowfloor:\n  response:\n    maxBytes: 4095\n")
	if cfg, _, err := NewResolver(dir, nil).Resolve("acme/atfloor"); err != nil {
		t.Errorf("maxBytes at floor (4096) rejected: %v", err)
	} else if cfg.Response.MaxBytes != 4096 {
		t.Errorf("Response.MaxBytes = %d, want 4096", cfg.Response.MaxBytes)
	}
	if _, _, err := NewResolver(dir, nil).Resolve("acme/belowfloor"); err == nil {
		t.Error("maxBytes one below floor (4095) accepted, want rejection")
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

func TestResolveTrimsCategoryName(t *testing.T) {
	dir := t.TempDir()
	// A name with surrounding whitespace validates (the checks trim) but must also be
	// stored trimmed, so it doesn't leak into the reduction's output keys and paths.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  quality:\n    requiredCategories:\n      - name: \"type \"\n        labels: [bug]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Quality.RequiredCategories) != 1 || cfg.Quality.RequiredCategories[0].Name != "type" {
		t.Errorf("category name = %q, want %q (trimmed)", cfg.Quality.RequiredCategories[0].Name, "type")
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

func TestDefaultsIncludesOverlapThreshold(t *testing.T) {
	if d := Defaults(); d.Overlap.TitleSimilarityThreshold != 0.5 {
		t.Errorf("Defaults().Overlap.TitleSimilarityThreshold = %g, want 0.5", d.Overlap.TitleSimilarityThreshold)
	}
}

func TestResolveMergesOverlapThreshold(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  overlap:\n    titleSimilarityThreshold: 0.8\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Overlap.TitleSimilarityThreshold != 0.8 {
		t.Errorf("threshold = %g, want 0.8 (from manifest)", cfg.Overlap.TitleSimilarityThreshold)
	}
}

func TestResolveOverlapOmittedInheritsDefault(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Overlap.TitleSimilarityThreshold != 0.5 {
		t.Errorf("threshold = %g, want 0.5 (inherited default)", cfg.Overlap.TitleSimilarityThreshold)
	}
}

func TestResolveOverlapExplicitZeroDisables(t *testing.T) {
	dir := t.TempDir()
	// An explicit 0 (disable) must be distinguishable from an omitted field (which
	// inherits the 0.5 default) — the omitted-vs-explicit pointer idiom.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  overlap:\n    titleSimilarityThreshold: 0\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Overlap.TitleSimilarityThreshold != 0 {
		t.Errorf("threshold = %g, want 0 (explicit disable, not the default)", cfg.Overlap.TitleSimilarityThreshold)
	}
}

func TestResolveAcceptsOverlapThresholdBounds(t *testing.T) {
	// Both 0 and 1 are valid (disable and exact-match-only); only out-of-range fails.
	for _, v := range []string{"0", "1"} {
		dir := t.TempDir()
		writeManifest(t, dir, "repos.yml", "acme/widgets:\n  overlap:\n    titleSimilarityThreshold: "+v+"\n")
		if _, _, err := NewResolver(dir, nil).Resolve("acme/widgets"); err != nil {
			t.Errorf("threshold %s: unexpected error %v", v, err)
		}
	}
}

func TestResolveRejectsOverlapThresholdOutOfRange(t *testing.T) {
	// .nan is rejected explicitly: it passes a naive range check (every NaN
	// comparison is false) but would silently degrade linking to exact-match-only.
	for _, v := range []string{"1.5", "-0.1", ".nan"} {
		dir := t.TempDir()
		writeManifest(t, dir, "repos.yml", "acme/widgets:\n  overlap:\n    titleSimilarityThreshold: "+v+"\n")
		_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
		if err == nil {
			t.Errorf("threshold %s: want range error", v)
			continue
		}
		if !strings.Contains(err.Error(), "titleSimilarityThreshold") {
			t.Errorf("threshold %s: error %q does not name the field", v, err)
		}
	}
}

func TestDefaultsIncludesTrajectoryWindows(t *testing.T) {
	d := Defaults()
	if len(d.Trajectory.Windows) != 3 || d.Trajectory.Windows[0] != 7 || d.Trajectory.Windows[1] != 30 || d.Trajectory.Windows[2] != 90 {
		t.Errorf("Defaults().Trajectory.Windows = %v, want [7 30 90]", d.Trajectory.Windows)
	}
	if d.Trajectory.FetchLimit != 500 {
		t.Errorf("Defaults().Trajectory.FetchLimit = %d, want 500", d.Trajectory.FetchLimit)
	}
}

func TestResolveMergesTrajectoryWindows(t *testing.T) {
	dir := t.TempDir()
	// Sets windows only; fetchLimit must inherit the default.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  trajectory:\n    windows: [14, 60]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Trajectory.Windows) != 2 || cfg.Trajectory.Windows[0] != 14 || cfg.Trajectory.Windows[1] != 60 {
		t.Errorf("Windows = %v, want [14 60] (from manifest)", cfg.Trajectory.Windows)
	}
	if cfg.Trajectory.FetchLimit != 500 {
		t.Errorf("FetchLimit = %d, want 500 (inherited default)", cfg.Trajectory.FetchLimit)
	}
}

func TestResolveTrajectoryOmittedInheritsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Trajectory.Windows) != 3 || cfg.Trajectory.FetchLimit != 500 {
		t.Errorf("Trajectory = %+v, want default windows [7 30 90] and fetchLimit 500", cfg.Trajectory)
	}
}

func TestResolveRejectsInvalidTrajectory(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, wantField string
	}{
		{"non-positive window", "acme/widgets:\n  trajectory:\n    windows: [7, 0, 90]\n", "trajectory.windows[1]"},
		{"negative window", "acme/widgets:\n  trajectory:\n    windows: [-5]\n", "trajectory.windows[0]"},
		{"empty windows", "acme/widgets:\n  trajectory:\n    windows: []\n", "trajectory.windows"},
		{"non-positive fetchLimit", "acme/widgets:\n  trajectory:\n    fetchLimit: 0\n", "trajectory.fetchLimit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.manifest)
			_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
			if err == nil {
				t.Fatalf("%s: want validation error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("%s: error %q does not name %q", tc.name, err, tc.wantField)
			}
		})
	}
}

func TestDefaultsIncludesSummary(t *testing.T) {
	d := Defaults().Summary
	if d.PRStalenessDays != 14 || d.UnmilestonedAgeDays != 30 {
		t.Errorf("Summary thresholds = %d/%d, want 14/30", d.PRStalenessDays, d.UnmilestonedAgeDays)
	}
	if d.PRFetchLimit != 200 || d.MilestoneFetchLimit != 100 {
		t.Errorf("Summary fetch limits = %d/%d, want 200/100", d.PRFetchLimit, d.MilestoneFetchLimit)
	}
	if len(d.BugLabels) != 1 || d.BugLabels[0] != "bug" {
		t.Errorf("Summary.BugLabels = %v, want [bug]", d.BugLabels)
	}
}

func TestResolveMergesSummary(t *testing.T) {
	dir := t.TempDir()
	// Sets two fields; the rest must inherit their defaults.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  summary:\n    prStalenessDays: 7\n    bugLabels: [defect, regression]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Summary.PRStalenessDays != 7 {
		t.Errorf("PRStalenessDays = %d, want 7 (from manifest)", cfg.Summary.PRStalenessDays)
	}
	if len(cfg.Summary.BugLabels) != 2 || cfg.Summary.BugLabels[0] != "defect" {
		t.Errorf("BugLabels = %v, want [defect regression]", cfg.Summary.BugLabels)
	}
	if cfg.Summary.UnmilestonedAgeDays != 30 || cfg.Summary.PRFetchLimit != 200 || cfg.Summary.MilestoneFetchLimit != 100 {
		t.Errorf("inherited fields = %+v, want 30/200/100", cfg.Summary)
	}
}

func TestResolveSummaryOmittedInheritsDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	d := Defaults().Summary
	if cfg.Summary.PRStalenessDays != d.PRStalenessDays || cfg.Summary.UnmilestonedAgeDays != d.UnmilestonedAgeDays ||
		cfg.Summary.PRFetchLimit != d.PRFetchLimit || cfg.Summary.MilestoneFetchLimit != d.MilestoneFetchLimit ||
		len(cfg.Summary.BugLabels) != 1 {
		t.Errorf("Summary = %+v, want defaults %+v", cfg.Summary, d)
	}
}

func TestResolveSummaryExplicitEmptyBugLabelsOptsOut(t *testing.T) {
	dir := t.TempDir()
	// An explicit empty list opts the repo out of bug flagging (distinct from omitted,
	// which inherits the "bug" default).
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  summary:\n    bugLabels: []\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Summary.BugLabels) != 0 {
		t.Errorf("BugLabels = %v, want empty (explicit opt-out)", cfg.Summary.BugLabels)
	}
}

func TestResolveRejectsInvalidSummary(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, wantField string
	}{
		{"zero prStalenessDays", "acme/widgets:\n  summary:\n    prStalenessDays: 0\n", "summary.prStalenessDays"},
		{"negative unmilestonedAgeDays", "acme/widgets:\n  summary:\n    unmilestonedAgeDays: -1\n", "summary.unmilestonedAgeDays"},
		{"zero prFetchLimit", "acme/widgets:\n  summary:\n    prFetchLimit: 0\n", "summary.prFetchLimit"},
		{"zero milestoneFetchLimit", "acme/widgets:\n  summary:\n    milestoneFetchLimit: 0\n", "summary.milestoneFetchLimit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.manifest)
			_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
			if err == nil {
				t.Fatalf("%s: want validation error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("%s: error %q does not name %q", tc.name, err, tc.wantField)
			}
		})
	}
}

func TestResolveMilestoneTracksDefaults(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets: {}\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	mt := cfg.MilestoneTracks
	if len(mt.HeadingLevels) != 2 || mt.HeadingLevels[0] != 2 || mt.HeadingLevels[1] != 3 {
		t.Errorf("HeadingLevels = %v, want [2 3]", mt.HeadingLevels)
	}
	if !mt.BoldRunIn {
		t.Error("BoldRunIn = false, want true (default)")
	}
	if mt.FetchLimit != 100 {
		t.Errorf("FetchLimit = %d, want 100 (default)", mt.FetchLimit)
	}
	if len(mt.LabelStoplist) == 0 {
		t.Error("LabelStoplist is empty, want the default prose-section labels")
	}
}

func TestResolveMilestoneTracksOverrides(t *testing.T) {
	dir := t.TempDir()
	// headingLevels and labelStoplist replace wholesale; boldRunIn:false disables;
	// fetchLimit overrides; an omitted field would inherit (not exercised here).
	writeManifest(t, dir, "repos.yml",
		"acme/widgets:\n  milestoneTracks:\n    headingLevels: [2]\n    boldRunIn: false\n    fetchLimit: 25\n    labelStoplist: [Notes]\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	mt := cfg.MilestoneTracks
	if len(mt.HeadingLevels) != 1 || mt.HeadingLevels[0] != 2 {
		t.Errorf("HeadingLevels = %v, want [2]", mt.HeadingLevels)
	}
	if mt.BoldRunIn {
		t.Error("BoldRunIn = true, want false (explicit disable, distinct from omission)")
	}
	if mt.FetchLimit != 25 {
		t.Errorf("FetchLimit = %d, want 25", mt.FetchLimit)
	}
	if len(mt.LabelStoplist) != 1 || mt.LabelStoplist[0] != "Notes" {
		t.Errorf("LabelStoplist = %v, want [Notes] (whole-list replace)", mt.LabelStoplist)
	}
}

func TestResolveMilestoneTracksEmptyHeadingLevelsDisables(t *testing.T) {
	dir := t.TempDir()
	// An explicit empty headingLevels is a valid disable (not a mistake), since
	// boldRunIn is an independent marker source — unlike trajectory.windows.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  milestoneTracks:\n    headingLevels: []\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.MilestoneTracks.HeadingLevels) != 0 {
		t.Errorf("HeadingLevels = %v, want empty (explicit disable)", cfg.MilestoneTracks.HeadingLevels)
	}
}

func TestResolveRejectsInvalidMilestoneTracks(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, wantField string
	}{
		{"heading level zero", "acme/widgets:\n  milestoneTracks:\n    headingLevels: [0]\n", "milestoneTracks.headingLevels"},
		{"heading level above six", "acme/widgets:\n  milestoneTracks:\n    headingLevels: [7]\n", "milestoneTracks.headingLevels"},
		{"zero fetchLimit", "acme/widgets:\n  milestoneTracks:\n    fetchLimit: 0\n", "milestoneTracks.fetchLimit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.manifest)
			_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
			if err == nil {
				t.Fatalf("%s: want validation error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("%s: error %q does not name %q", tc.name, err, tc.wantField)
			}
		})
	}
}

func TestResolveMergesCriticalPath(t *testing.T) {
	dir := t.TempDir()
	// Leading/trailing whitespace on a stream name and the label must be trimmed at
	// the resolution boundary, else the stored value never matches the matcher's
	// trimmed canonical name.
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  criticalPath:\n    streams: [\" simulation \", narrative]\n    label: \" critical-path \"\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.CriticalPath.Streams) != 2 || cfg.CriticalPath.Streams[0] != "simulation" || cfg.CriticalPath.Streams[1] != "narrative" {
		t.Errorf("Streams = %q, want [simulation narrative] (trimmed)", cfg.CriticalPath.Streams)
	}
	if cfg.CriticalPath.Label != "critical-path" {
		t.Errorf("Label = %q, want critical-path (trimmed)", cfg.CriticalPath.Label)
	}
}

func TestResolveNoEntryLeavesCriticalPathEmpty(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 30\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.CriticalPath.Streams) != 0 || cfg.CriticalPath.Label != "" {
		t.Errorf("CriticalPath = %+v, want empty (not configured)", cfg.CriticalPath)
	}
}

func TestResolveRejectsInvalidCriticalPath(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, wantField string
	}{
		{"streams without label", "acme/widgets:\n  criticalPath:\n    streams: [simulation]\n", "criticalPath.label"},
		{"label without streams", "acme/widgets:\n  criticalPath:\n    label: critical-path\n", "criticalPath.streams"},
		{"explicit empty streams with label", "acme/widgets:\n  criticalPath:\n    streams: []\n    label: critical-path\n", "criticalPath.streams"},
		{"duplicate streams", "acme/widgets:\n  criticalPath:\n    streams: [simulation, Simulation]\n    label: critical-path\n", "duplicate"},
		{"empty stream name", "acme/widgets:\n  criticalPath:\n    streams: [simulation, \"  \"]\n    label: critical-path\n", "criticalPath.streams"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "repos.yml", tc.manifest)
			_, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
			if err == nil {
				t.Fatalf("%s: want validation error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("%s: error %q does not name %q", tc.name, err, tc.wantField)
			}
		})
	}
}
