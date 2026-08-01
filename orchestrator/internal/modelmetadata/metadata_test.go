package modelmetadata

import "testing"

func TestLookupGeneratedModelsDevMetadata(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		modelID    string
		context    int
		reasoning  bool
		tools      bool
		image      bool
	}{
		{name: "glm 5.2 coding plan", providerID: "zhipuai-coding-plan", modelID: "glm-5.2", context: 1_000_000, reasoning: true, tools: true},
		{name: "glm 5 turbo coding plan", providerID: "zhipuai-coding-plan", modelID: "glm-5-turbo", context: 200_000, reasoning: true, tools: true},
		{name: "qwen 3.8 max preview", providerID: "alibaba-token-plan-cn", modelID: "qwen3.8-max-preview", context: 1_000_000, reasoning: true, tools: true, image: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Lookup(tt.providerID, tt.modelID)
			if !ok {
				t.Fatalf("Lookup(%q, %q) not found", tt.providerID, tt.modelID)
			}
			if got.ContextWindow != tt.context || got.Capabilities.Reasoning != tt.reasoning ||
				got.Capabilities.Tools != tt.tools || got.Capabilities.Image != tt.image {
				t.Fatalf("metadata = %+v, want context=%d reasoning=%v tools=%v image=%v",
					got, tt.context, tt.reasoning, tt.tools, tt.image)
			}
		})
	}
}

func TestLookupDoesNotGuessUnknownProviderOrModel(t *testing.T) {
	if _, ok := Lookup("custom-openai-compatible", "glm-5.2"); ok {
		t.Fatal("custom provider must not inherit another provider's metadata")
	}
	if _, ok := Lookup("zhipuai-coding-plan", "glm-unknown"); ok {
		t.Fatal("unknown model must remain unknown")
	}
}
