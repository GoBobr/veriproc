// Command veriprocd runs the VeriProc backend HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi"
	"github.com/eum/veriproc/internal/logging"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
	"github.com/eum/veriproc/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "veriprocd: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load(args, config.EnvSnapshot())
	if err != nil {
		return err
	}

	logger := logging.New(cfg.Log)
	logger.Info().
		Str("instance_id", cfg.InstanceID).
		Str("version", version.Version).
		Str("api_version", version.APIVersion).
		Str("bind_addr", cfg.HTTP.BindAddr).
		Msg("veriprocd starting")

	agg := health.NewAggregator(2 * time.Second)
	// Milestone 0 ships with no checkers; later milestones register their own.

	// Persistence + task service (M1 + M2). When no DSN is configured we fall
	// back to an in-memory SQLite database so the daemon still starts in
	// developer setups.
	dsn := cfg.DB.DSN
	if dsn == "" {
		dsn = "sqlite://:memory:"
	}
	st, err := store.Open(dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	if err := store.Migrate(context.Background(), st); err != nil {
		return fmt.Errorf("migrate store: %w", err)
	}

	registry := stations.NewRegistry()
	// M2 ships with no built-in stations; deployments / tests seed them.
	taskSvc := tasks.NewService(st, registry, nil, nil)

	router := httpapi.NewRouter(httpapi.Deps{
		Config: cfg,
		Health: agg,
		Logger: logger,
		Tasks:  taskSvc,
	})

	srv := &http.Server{
		Addr:         cfg.HTTP.BindAddr,
		Handler:      router,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		logger.Info().Stringer("signal", sig).Msg("shutdown signal received")
	case err, ok := <-errCh:
		if ok && err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}

	agg.BeginShutdown()

	shutdownTimeout := cfg.HTTP.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info().Msg("veriprocd stopped cleanly")
	return nil
}
