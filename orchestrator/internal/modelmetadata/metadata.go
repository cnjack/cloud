// Package modelmetadata exposes the build-time models.dev snapshot used for
// deterministic provider catalogs and configured-model metadata.
package modelmetadata

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"

	"github.com/cnjack/jcloud/internal/domain"
)

//go:generate go run ./generate

// Metadata is the subset of models.dev that jcloud persists for a configured
// model. The registry deliberately omits pricing and generation controls because
// those are not part of the model-provider API contract.
type Metadata struct {
	Name          string                   `json:"name"`
	ContextWindow int                      `json:"context_window"`
	Capabilities  domain.ModelCapabilities `json:"capabilities"`
}

// Entry is one model in a provider's build-time catalog.
type Entry struct {
	ID string
	Metadata
}

//go:embed metadata_generated.json
var generatedJSON []byte

var (
	loadOnce sync.Once
	registry map[string]map[string]Metadata
)

func load() {
	loadOnce.Do(func() {
		if err := json.Unmarshal(generatedJSON, &registry); err != nil {
			panic("decode generated models.dev metadata: " + err.Error())
		}
	})
}

// Lookup returns exact provider/model metadata. It intentionally performs no
// cross-provider or fuzzy-name guessing: a custom OpenAI-compatible endpoint
// must not inherit capabilities merely because its model id resembles a known
// provider's id.
func Lookup(providerID, modelID string) (Metadata, bool) {
	load()
	models, ok := registry[providerID]
	if !ok {
		return Metadata{}, false
	}
	metadata, ok := models[modelID]
	return metadata, ok
}

// List returns the complete build-time catalog for an exact provider ID. The
// stable model-ID ordering keeps API responses and UI rendering deterministic.
// Unknown providers return (nil, false) so callers can probe their live
// OpenAI-compatible /models endpoint instead.
func List(providerID string) ([]Entry, bool) {
	load()
	models, ok := registry[providerID]
	if !ok {
		return nil, false
	}
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, Entry{ID: id, Metadata: models[id]})
	}
	return entries, true
}
