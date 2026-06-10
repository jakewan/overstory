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
