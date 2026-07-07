// Command orchestrator is the jcode Cloud Agent control-plane server. It serves
// the REST/SSE API, runs the idempotent reconciler, and schedules runner Jobs
// on Kubernetes. All configuration comes from the environment (see .env.example).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cnjack/jcloud/internal/api"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/k8s"
	"github.com/cnjack/jcloud/internal/provider"
	"github.com/cnjack/jcloud/internal/reconciler"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
	"github.com/cnjack/jcloud/internal/version"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	migrateOnly := flag.Bool("migrate-only", false, "apply DB migrations and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("starting orchestrator",
		"version", version.Version, "commit", version.Commit,
		"addr", cfg.ListenAddr, "namespace", cfg.Namespace,
		"k8s_disabled", cfg.DisableK8s, "max_concurrent", cfg.MaxConcurrentRuns)

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- store + migrations ---
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := store.Migrate(ctx, st.Pool()); err != nil {
		return err
	}
	log.Info("migrations applied")

	if *migrateOnly {
		log.Info("migrate-only: exiting after migrations")
		return nil
	}

	// --- realtime hub ---
	hub := sse.NewHub()

	// --- launcher (optional) ---
	var launcher k8s.JobLauncher
	switch {
	case cfg.DisableK8s:
		log.Warn("launcher disabled (DISABLE_K8S=1): runs will queue but not schedule")
	case cfg.JobLauncher == "process":
		// Local dev / full-loop integration: run each runner as a docker container.
		log.Info("using process launcher (docker run)", "image", cfg.RunnerImage, "network", cfg.RunnerNetwork)
		launcher = k8s.NewProcessLauncher(k8s.ProcessConfig{
			Image:     cfg.RunnerImage,
			Network:   cfg.RunnerNetwork,
			ExtraArgs: cfg.RunnerDockerArgs,
		})
	default:
		client, err := k8s.NewClient(k8s.Config{
			Kubeconfig:     cfg.Kubeconfig,
			Namespace:      cfg.Namespace,
			RunnerImage:    cfg.RunnerImage,
			ServiceAccount: cfg.ServiceAccount,
			TTLSeconds:     cfg.JobTTLSeconds,
			CPULimit:       cfg.CPULimit,
			MemoryLimit:    cfg.MemoryLimit,
			CPURequest:     cfg.CPURequest,
			MemoryRequest:  cfg.MemoryRequest,
		})
		if err != nil {
			return err
		}
		launcher = client
	}

	// --- HTTP server ---
	// Built before the reconciler so the two share one credential resolver + git
	// wrapper (the source endpoint and the reconcile push/review passes act with
	// the same tokens and binary).
	srv := api.New(st, cfg, log, hub, launcher)

	// --- reconciler ---
	// The draft-PR / review passes push branches + open PRs + post reviews on the
	// triggering user's behalf (or the fallback gitea PAT). The provider Factory
	// builds a PR client per resolved token; gitcli pushes; the credential
	// resolver is shared with the API. A deployment without git or a provider
	// simply degrades to diff-only (each pass is a no-op).
	if launcher != nil {
		factory := provider.NewFactory(cfg.GiteaURL)
		rec := reconciler.New(st, launcher, cfg, log, hub).
			WithPRStack(factory, srv.Git(), srv.Credentials()).
			// Share the API's model resolver so a console PUT/DELETE invalidates
			// the SAME cache the scheduler resolves through (Feature A).
			WithModelResolver(srv.Models())
		if !srv.Git().Available() {
			log.Warn("git binary not found: draft-PR push + source bundling disabled")
		} else {
			log.Info("draft-PR / review stack enabled", "gitea_url", cfg.GiteaURL)
		}
		go rec.Run(ctx)
	}

	// streamCtx is the BaseContext for every request, so an SSE handler's
	// r.Context() derives from it. http.Server.Shutdown only closes IDLE
	// connections; a long-lived SSE stream blocks on its request context, which
	// Shutdown does NOT cancel. Cancelling streamCtx on shutdown unblocks those
	// handlers so they write a final `: server shutting down` comment and return,
	// instead of Shutdown waiting the full timeout and returning a deadline error
	// that looks like a crash on every rollout with a connected console.
	streamCtx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return streamCtx },
		// No WriteTimeout: SSE streams are long-lived.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	// Unblock in-flight SSE streams so they finish promptly, then drain the rest.
	cancelStreams()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		// A deadline here means some connection did not drain in time; log it as a
		// warning rather than a fatal exit so a clean rollout is not reported as a
		// crash.
		log.Warn("graceful shutdown did not complete cleanly", "err", err)
	}
	return nil
}
