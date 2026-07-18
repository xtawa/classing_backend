package aicost

import "testing"

func TestProCostsMoreThanFlash(t *testing.T) {
	usage := TokenUsage{InputTokens: 1200, CachedInputTokens: 200, OutputTokens: 500}
	flash := Points("deepseek-v4-flash", usage)
	pro := Points("deepseek-v4-pro", usage)
	if flash <= 0 || pro <= flash {
		t.Fatalf("unexpected points flash=%d pro=%d", flash, pro)
	}
}

func TestLegacyModelDefaultsToFlash(t *testing.T) {
	for _, id := range []string{"", "deepseek-chat", "deepseek-reasoner"} {
		model, ok := Resolve(id)
		if !ok || model.ID != DefaultModel {
			t.Fatalf("legacy model %q did not resolve to default", id)
		}
	}
}
