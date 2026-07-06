// Command orchestrator is the jcode Cloud Agent control-plane server. It serves
// the REST/SSE API, runs the idempotent reconciler, and schedules runner Jobs
// on Kubernetes. All configuration comes from the environment (see .env.example).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cnjack/jcloud/internal/api"
	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/k8s"
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

	// --- k8s launcher (optional) ---
	var launcher k8s.JobLauncher
	if cfg.DisableK8s {
		log.Warn("K8s disabled (DISABLE_K8S=1): runs will queue but not schedule")
	} else {
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

	// --- reconciler ---
	if launcher != nil {
		rec := reconciler.New(st, launcher, cfg, log, hub)
		go rec.Run(ctx)
	}

	// --- HTTP server ---
	srv := api.New(st, cfg, log, hub, launcher)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
