// Package modelcfg resolves the EFFECTIVE cluster LLM configuration from the two
// sources that can supply it — a cluster admin's console-set DB row (preferred)
// and the MODEL_* environment variables (fallback) — into a single answer the
// API gate, the reconciler's job env, and the console status badge all share.
//
// Fail-visible (CLAUDE.md red line #1): when neither source is configured,
// Resolve reports source="none" so callers surface an honest "LLM not
// configured" state instead of silently substituting a mock. There is NO mock
// default here.
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
	// SourceDB: an admin-set cluster_model_config row (takes precedence).
	SourceDB Source = "db"
	// SourceEnv: the MODEL_* environment variables (all three non-empty).
	SourceEnv Source = "env"
	// SourceNone: nothing is configured — a run must fail-visible, not mock.
	SourceNone Source = "none"
)

// Resolved is the effective model configuration. APIKey is the DECRYPTED key
// (empty when the endpoint needs none); callers must NEVER serialise it to API
// clients — expose only APIKeySet.
type Resolved struct {
	Source    Source
	BaseURL   string
	ModelName string
	APIKey    string
	APIKeySet bool
}

// Configured reports whether a usable model config was resolved.
func (r Resolved) Configured() bool { return r.Source != SourceNone }

// ConfigReader is the slice of the store Resolve needs (a MemStore/PGStore, or a
// test stub). It is the seam the resolver depends on.
type ConfigReader interface {
	GetModelConfig(ctx context.Context) (*domain.ModelConfig, error)
}

// Resolve computes the effective model config: the DB row wins (its api_key_enc
// is decrypted with cipher), else the MODEL_* env when ALL THREE (base_url,
// model_name, api_key) are non-empty, else source="none".
//
// A DB row with an api_key_enc blob but no cipher (AUTH_TOKEN_KEY unset after a
// key was stored) is an operator error we surface as an error rather than
// silently dropping the key — the run should fail-visible, not act key-less.
func Resolve(ctx context.Context, st ConfigReader, cipher *auth.Cipher, cfg *config.Config) (Resolved, error) {
	row, err := st.GetModelConfig(ctx)
	switch {
	case err == nil:
		out := Resolved{Source: SourceDB, BaseURL: row.BaseURL, ModelName: row.ModelName}
		if len(row.APIKeyEnc) > 0 {
			key, derr := cipher.DecryptString(row.APIKeyEnc)
			if derr != nil {
				return Resolved{}, derr
			}
			out.APIKey = key
			out.APIKeySet = true
		}
		return out, nil
	case errors.Is(err, store.ErrNotFound):
		// Fall through to the env fallback below.
	default:
		return Resolved{}, err
	}

	// Env fallback: "configured" when BOTH base_url and model_name are present —
	// the API key may legitimately be empty (keyless OpenAI-compatible endpoints),
	// matching the DB path's keyless semantics. A partial env (only one of the
	// two) never masquerades as configured.
	if cfg != nil && cfg.ModelBaseURL != "" && cfg.ModelName != "" {
		return Resolved{
			Source:    SourceEnv,
			BaseURL:   cfg.ModelBaseURL,
			ModelName: cfg.ModelName,
			APIKey:    cfg.ModelAPIKey,
			APIKeySet: cfg.ModelAPIKey != "",
		}, nil
	}
	return Resolved{Source: SourceNone}, nil
}

// NotConfiguredMessage is the ONE user-facing explanation for the fail-visible
// "no LLM configured" state, shared by the API 409, the reconciler's failure
// message, and the webhook PR reply so the wording never drifts. consoleURL,
// when non-empty, is appended as the place to go fix it (used on surfaces that
// cannot link, e.g. a PR comment).
func NotConfiguredMessage(consoleURL string) string {
	msg := "the LLM is not configured — a cluster admin must set it on the Cluster page before runs can start"
	if consoleURL != "" {
		msg += ": " + strings.TrimRight(consoleURL, "/")
	}
	return msg
}
