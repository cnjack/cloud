// Package modelcfg resolves the EFFECTIVE LLM configuration for a run from the
// model catalog (D21) and the MODEL_* environment fallback, into the single
// answer the API gate, the reconciler's job env, the LLM reverse proxy and the
// console status all share.
//
// Two distinct operations (deliberately separate):
//
//   - SELECTION (SelectModel): run the resolution chain at run-create time to
//     CHOOSE which catalog model a new run uses — composer pick → service
//     default → the project's sole granted model → typed fail-visible errors. The
//     chosen model id is stamped on runs.model_id.
//   - MATERIALISATION (ResolveModel): turn a stamped run.model_id (or the env
//     fallback) into the concrete base_url/model_name/decrypted key the
//     reconciler + proxy forward with. Runs on the hot path, so it is cached
//     (see resolver.go).
//
// Fail-visible (CLAUDE.md red line #1): every unconfigured/ambiguous state is a
// TYPED error the caller surfaces honestly (409 model_not_configured / 409
// model_not_selected / 403 model_not_granted), never a silent mock. The MODEL_*
// env fallback applies ONLY when the catalog is EMPTY (local rig compatibility);
// a non-empty catalog with zero grants for a project is model_not_configured, not
// an env fallback. There is NO mock default here.
package modelcfg

import (
	"context"
	"errors"
	"strings"

	"github.com/cnjack/jcloud/internal/auth"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// Source labels where the effective config came from.
type Source string

const (
	// SourceCatalog: a catalog model (runs.model_id resolved to it).
	SourceCatalog Source = "catalog"
	// SourceEnv: the MODEL_* environment variables (empty-catalog fallback).
	SourceEnv Source = "env"
	// SourceNone: nothing usable — a run must fail-visible, not mock.
	SourceNone Source = "none"
)

// Resolved is the effective, materialised model configuration for a run. APIKey
// is the DECRYPTED key (empty when the endpoint needs none); callers must NEVER
// serialise it to API clients — expose only APIKeySet.
type Resolved struct {
	Source    Source
	ModelID   string // catalog model id; "" for the env fallback / none
	BaseURL   string
	ModelName string
	APIKey    string
	APIKeySet bool
}

// Configured reports whether a usable model config was materialised.
func (r Resolved) Configured() bool { return r.Source != SourceNone }

// SelectOutcome is the machine-readable result of the resolution chain.
type SelectOutcome int

const (
	// SelectOK: a model was chosen (ModelID; "" means the env fallback).
	SelectOK SelectOutcome = iota
	// SelectNotConfigured: the project has no granted model and no env fallback
	// applies (409 model_not_configured — "contact an admin to grant one").
	SelectNotConfigured
	// SelectNotSelected: several models are granted but none was picked and the
	// service has no default (409 model_not_selected — pick one / set a default).
	SelectNotSelected
	// SelectNotGranted: the composer requested a model not granted to the project
	// (403 model_not_granted).
	SelectNotGranted
)

// ConfigReader is the slice of the store the resolver needs (a store.Store or a
// test stub). It is the seam the resolver depends on.
type ConfigReader interface {
	GetModel(ctx context.Context, id string) (*domain.Model, error)
	ListModelsForProject(ctx context.Context, projectID string) ([]domain.Model, error)
	CountModels(ctx context.Context) (int, error)
}

// envConfigured reports whether the MODEL_* env fallback is fully set (both
// base_url and model_name; the key may legitimately be empty for keyless
// OpenAI-compatible endpoints).
func envConfigured(cfg *config.Config) bool {
	return cfg != nil && cfg.ModelBaseURL != "" && cfg.ModelName != ""
}

// resolveModel materialises a stamped model id (or the env fallback when
// modelID == "") into a Resolved. A modelID that no longer exists in the catalog
// (the admin deleted it after the run was queued) resolves to source="none" so
// the caller fails visibly rather than launching key-less.
//
// The empty-modelID branch is the env fallback, but ONLY when the catalog is
// EMPTY — exactly the condition selectModel used when it chose "" at create time.
// This closes the C1 hole: a run whose model was deleted has its runs.model_id
// FK-nulled to "", and without the CountModels==0 guard it would silently resolve
// to the MODEL_* env model (on the e2e rig, the mockllm fake output). With the
// guard, a non-empty catalog + a NULL model_id is source="none" → the caller
// fails visibly (reconciler setup_failed / proxy 503). Known accepted edge: a run
// selected via the env fallback (empty catalog) that is scheduled after an admin
// adds the FIRST model now materialises to source="none" and setup_fails — rare
// and visible, never a silent mock.
//
// A catalog model with an api_key_enc blob but no cipher (AUTH_TOKEN_KEY unset
// after a key was stored) is an operator error surfaced as an error rather than
// silently dropping the key — the run should fail-visible, not act key-less.
func resolveModel(ctx context.Context, st ConfigReader, cipher *auth.Cipher, cfg *config.Config, modelID string) (Resolved, error) {
	if modelID != "" {
		m, err := st.GetModel(ctx, modelID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return Resolved{Source: SourceNone}, nil
		case err != nil:
			return Resolved{}, err
		}
		out := Resolved{Source: SourceCatalog, ModelID: m.ID, BaseURL: m.BaseURL, ModelName: m.ModelName}
		if len(m.APIKeyEnc) > 0 {
			key, derr := cipher.DecryptString(m.APIKeyEnc)
			if derr != nil {
				return Resolved{}, derr
			}
			out.APIKey = key
			out.APIKeySet = true
		}
		return out, nil
	}

	// Env fallback: only when the catalog is EMPTY (mirrors selectModel), and both
	// base_url and model_name are present (the API key may legitimately be empty
	// for keyless endpoints). A non-empty catalog with a NULL model_id means the
	// run's model was deleted (FK SET NULL) — that is source="none", NEVER a
	// silent env fallback.
	if envConfigured(cfg) {
		total, err := st.CountModels(ctx)
		if err != nil {
			return Resolved{}, err
		}
		if total == 0 {
			return Resolved{
				Source:    SourceEnv,
				BaseURL:   cfg.ModelBaseURL,
				ModelName: cfg.ModelName,
				APIKey:    cfg.ModelAPIKey,
				APIKeySet: cfg.ModelAPIKey != "",
			}, nil
		}
	}
	return Resolved{Source: SourceNone}, nil
}

// Selection is the outcome of the resolution chain: the chosen catalog model id
// ("" for the env fallback) plus the model's provider/model NAME, snapshotted
// onto runs.model_name for audit (a run stays traceable to its model even after
// the model is deleted from the catalog). For the env fallback ModelID is "" and
// ModelName is the env model name.
type Selection struct {
	ModelID   string
	ModelName string
}

// selectModel runs the resolution chain to CHOOSE a model for a new run:
//
//	requested (composer pick) → defaultModelID (service default) → the project's
//	sole granted model → typed outcome.
//
// The returned Selection.ModelID is "" when the run resolves to the env fallback
// (an empty catalog). requested/defaultModelID must be granted to the project to
// be honoured (a stale service default whose grant was revoked is skipped, not an
// error).
func selectModel(ctx context.Context, st ConfigReader, cfg *config.Config, projectID, defaultModelID, requested string) (Selection, SelectOutcome, error) {
	grants, err := st.ListModelsForProject(ctx, projectID)
	if err != nil {
		return Selection{}, SelectOK, err
	}
	byID := make(map[string]domain.Model, len(grants))
	for i := range grants {
		byID[grants[i].ID] = grants[i]
	}
	pick := func(id string) Selection { return Selection{ModelID: id, ModelName: byID[id].ModelName} }

	// 1) Composer pick — must be in the project's grant set.
	if requested != "" {
		if _, ok := byID[requested]; !ok {
			return Selection{}, SelectNotGranted, nil
		}
		return pick(requested), SelectOK, nil
	}
	// 2) Service default — honoured only while still granted.
	if _, ok := byID[defaultModelID]; ok && defaultModelID != "" {
		return pick(defaultModelID), SelectOK, nil
	}
	// 3) The project's granted set.
	switch {
	case len(grants) == 1:
		return pick(grants[0].ID), SelectOK, nil
	case len(grants) >= 2:
		return Selection{}, SelectNotSelected, nil
	default: // zero grants
		// Env fallback applies ONLY when the catalog is empty (local rig).
		total, err := st.CountModels(ctx)
		if err != nil {
			return Selection{}, SelectOK, err
		}
		if total == 0 && envConfigured(cfg) {
			// Env fallback: model_id stays NULL; snapshot the env model NAME.
			return Selection{ModelName: cfg.ModelName}, SelectOK, nil
		}
		return Selection{}, SelectNotConfigured, nil
	}
}

// NotConfiguredMessage is the shared fail-visible explanation for a project with
// no usable model (SelectNotConfigured / a materialisation that found none).
// consoleURL, when non-empty, is appended as where to go (surfaces that can't
// link, e.g. a PR comment).
func NotConfiguredMessage(consoleURL string) string {
	msg := "no LLM is configured for this project — contact a cluster admin to grant a model before runs can start"
	if consoleURL != "" {
		msg += ": " + strings.TrimRight(consoleURL, "/")
	}
	return msg
}

// NotSelectedMessage is the fail-visible explanation when several models are
// granted but none was picked and the service has no default.
func NotSelectedMessage() string {
	return "several models are available — pick one for this run or set a default model on the service"
}

// NotGrantedMessage is the fail-visible explanation when the composer requested a
// model the project is not authorized to use.
func NotGrantedMessage() string {
	return "the selected model is not authorized for this project"
}

// NotGrantedReuseMessage is the fail-visible explanation when a retry/review would
// REUSE a prior run's model that is no longer granted to the project. It points at
// the fix (set a service default, or pick another) rather than blaming a pick the
// user did not just make.
func NotGrantedReuseMessage() string {
	return "the model this run used is no longer authorized for this project — set a default model on the service or pick another"
}
