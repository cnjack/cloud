package schedule

import (
	"context"
	"errors"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// integrationHostGate is the production HostGate: it loads a service's bound
// integration and checks its host against the cluster allowlist (D20 / F5) via
// the SHARED domain.GitHostAllowed — the same function the API's dispatch gate
// wraps — so the poller need not depend on the api package and the two checks
// share one implementation.
type integrationHostGate struct {
	st           store.Store
	allowedHosts []string
}

// NewHostGate builds the default HostGate from the store and the cluster
// ALLOWED_GIT_HOSTS list. An empty allowlist means "all hosts allowed".
func NewHostGate(st store.Store, allowedHosts []string) HostGate {
	return integrationHostGate{st: st, allowedHosts: allowedHosts}
}

func (g integrationHostGate) IntegrationHostAllowed(ctx context.Context, svc *domain.Service) (bool, string, error) {
	if svc.IntegrationID == nil || *svc.IntegrationID == "" {
		return true, "", nil // legacy / unbound service — no integration host to gate
	}
	in, err := g.st.GetIntegration(ctx, *svc.IntegrationID)
	if errors.Is(err, store.ErrNotFound) {
		// The integration was removed (services.integration_id ON DELETE SET NULL may
		// not have propagated to this in-flight svc copy). Treat as unbound — the run
		// falls back to the legacy path, exactly like the API gate.
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}
	// The SHARED allowlist check (domain.GitHostAllowed) — the exact function the
	// API's dispatch gate wraps, so the two security-sensitive checks cannot drift.
	return domain.GitHostAllowed(g.allowedHosts, in.Host), in.Host, nil
}
