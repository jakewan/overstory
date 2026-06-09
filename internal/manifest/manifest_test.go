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

func TestResolveLastLoadedWins(t *testing.T) {
	dir := t.TempDir()
	// Sorted glob loads a-repos before b-repos; the later file's entry wins.
	writeManifest(t, dir, "a-repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 10\n")
	writeManifest(t, dir, "b-repos.yml", "acme/widgets:\n  staleness:\n    thresholdDays: 20\n")
	cfg, _, err := NewResolver(dir, nil).Resolve("acme/widgets")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Staleness.ThresholdDays != 20 {
		t.Errorf("ThresholdDays = %d, want 20 (last-loaded wins)", cfg.Staleness.ThresholdDays)
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
	dir := t.TempDir()
	writeManifest(t, dir, "repos.yml", "justaname:\n  staleness:\n    thresholdDays: 45\n")
	if _, _, err := NewResolver(dir, nil).Resolve("justaname"); err == nil {
		t.Error("Resolve accepted a malformed repo key, want error")
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
