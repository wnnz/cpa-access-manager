package plugin

import "testing"

func TestLiteLLMPriceRulesKeepsOnlyOfficialProviders(t *testing.T) {
	specs := map[string]liteLLMPriceSpec{
		"gpt-5.5": {
			Provider:           "openai",
			InputCostPerToken:  0.000005,
			OutputCostPerToken: 0.00003,
		},
		"azure/gpt-5.5": {
			Provider:           "azure",
			InputCostPerToken:  0.000005,
			OutputCostPerToken: 0.00003,
		},
		"ai21/j2-ultra": {
			Provider:           "ai21",
			InputCostPerToken:  0.000015,
			OutputCostPerToken: 0.000015,
		},
		"anthropic/claude-test": {
			Provider:           "anthropic",
			InputCostPerToken:  0.000003,
			OutputCostPerToken: 0.000015,
		},
		"gemini/gemini-test": {
			Provider:           "gemini",
			InputCostPerToken:  0.000001,
			OutputCostPerToken: 0.000004,
		},
		"vertex_ai/gemini-test": {
			Provider:           "vertex_ai",
			InputCostPerToken:  0.000001,
			OutputCostPerToken: 0.000004,
		},
		"vertex_ai/anthropic/claude-test": {
			Provider:           "vertex_ai",
			InputCostPerToken:  0.000003,
			OutputCostPerToken: 0.000015,
		},
	}

	rules := liteLLMPriceRules(specs)
	want := map[string]bool{
		"openai\x00gpt-5.5":        false,
		"anthropic\x00claude-test": false,
		"gemini\x00gemini-test":    false,
		"vertex\x00gemini-test":    false,
	}
	for _, rule := range rules {
		if rule.Provider == "azure" || rule.Provider == "ai21" || rule.Provider == "codex" || rule.Provider == "claude" || rule.Provider == "vertex_ai" {
			t.Fatalf("unexpected non-official rule: %#v", rule)
		}
		key := rule.Provider + "\x00" + rule.Model
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing official price rule %q", key)
		}
	}
}
