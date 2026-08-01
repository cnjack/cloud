// Command generate downloads models.dev and emits the compact, curated metadata
// snapshot embedded by the parent package.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"time"
)

const (
	modelsDevURL = "https://models.dev/api.json"
	outputFile   = "metadata_generated.json"
)

// Keep this list aligned with console/src/lib/desktopProviders.ts. The two
// jcode-maintained static providers are intentionally absent because their live
// endpoint IDs differ from models.dev; unknown providers stay explicitly
// unknown instead of receiving guessed metadata.
var wantedProviders = []string{
	"openai",
	"anthropic",
	"google",
	"deepseek",
	"zhipuai",
	"zhipuai-coding-plan",
	"mistral",
	"openrouter",
	"groq",
	"togetherai",
	"alibaba-cn",
	"alibaba-coding-plan-cn",
	"alibaba-token-plan-cn",
	"alibaba-token-plan",
	"moonshotai",
	"minimax",
	"minimax-coding-plan",
	"siliconflow",
	"tencent-coding-plan",
	"tencent-tokenhub",
	"zai",
	"zai-coding-plan",
	"xiaomi",
	"xiaomi-token-plan-cn",
	"ollama-cloud",
}

type provider struct {
	Models map[string]model `json:"models"`
}

type model struct {
	Name       string `json:"name"`
	Reasoning  bool   `json:"reasoning"`
	ToolCall   bool   `json:"tool_call"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
}

type metadata struct {
	Name          string       `json:"name"`
	ContextWindow int          `json:"context_window"`
	Capabilities  capabilities `json:"capabilities"`
}

type capabilities struct {
	Reasoning bool `json:"reasoning"`
	Tools     bool `json:"tools"`
	Image     bool `json:"image"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get(modelsDevURL) // #nosec G107 -- fixed public registry URL
	if err != nil {
		return fmt.Errorf("fetch models.dev: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch models.dev: %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read models.dev: %w", err)
	}
	var providers map[string]provider
	if err := json.Unmarshal(body, &providers); err != nil {
		return fmt.Errorf("decode models.dev: %w", err)
	}

	output := make(map[string]map[string]metadata, len(wantedProviders))
	for _, providerID := range wantedProviders {
		entry, ok := providers[providerID]
		if !ok {
			return fmt.Errorf("models.dev no longer contains required provider %q", providerID)
		}
		models := make(map[string]metadata, len(entry.Models))
		for modelID, source := range entry.Models {
			models[modelID] = metadata{
				Name:          source.Name,
				ContextWindow: source.Limit.Context,
				Capabilities: capabilities{
					Reasoning: source.Reasoning,
					Tools:     source.ToolCall,
					Image:     slices.Contains(source.Modalities.Input, "image"),
				},
			}
		}
		output[providerID] = models
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("encode metadata snapshot: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(outputFile, encoded, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputFile, err)
	}
	return nil
}
