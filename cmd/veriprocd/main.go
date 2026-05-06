// Command veriprocd runs the VeriProc backend HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eum/veriproc/internal/auth"
	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/groups"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi"
	"github.com/eum/veriproc/internal/logging"
	"github.com/eum/veriproc/internal/publisher"
	"github.com/eum/veriproc/internal/reconciler"
	"github.com/eum/veriproc/internal/runs"
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
	if cfg.Paths.StationConfigRoot != "" {
		specs, err := stations.LoadDir(context.Background(), cfg.Paths.StationConfigRoot, registry, st)
		if err != nil {
			return fmt.Errorf("load stations: %w", err)
		}
		logger.Info().Str("dir", cfg.Paths.StationConfigRoot).Int("count", len(specs)).Msg("loaded stations")
	}
	// Direct seeding remains useful for tests and tiny deployments.
	// Supported forms:
	//   station_id:proc_type
	//   station_id:proc_type:content_hash:schema_version
	if seed := os.Getenv("VERIPROC_SEED_STATIONS"); seed != "" {
		var specs []stations.Spec
		for _, entry := range strings.Split(seed, ";") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			parts := strings.Split(entry, ":")
			if len(parts) == 2 {
				spec, err := stations.SpecFromSeed(parts[0], parts[1])
				if err != nil {
					return fmt.Errorf("VERIPROC_SEED_STATIONS entry %q: %w", entry, err)
				}
				specs = append(specs, spec)
				continue
			}
			if len(parts) >= 4 {
				hash := strings.Join(parts[2:len(parts)-1], ":")
				specs = append(specs, stations.Spec{
					StationID: parts[0], ProcType: parts[1],
					ContentHash: hash, SchemaVersion: parts[len(parts)-1],
				})
				continue
			}
			return fmt.Errorf("VERIPROC_SEED_STATIONS entry %q: want station:proc or station:proc:hash:schema", entry)
		}
		if err := registry.Seed(context.Background(), st, specs...); err != nil {
			return fmt.Errorf("seed stations: %w", err)
		}
		logger.Info().Int("count", len(specs)).Msg("seeded stations")
	}
	taskSvc := tasks.NewService(st, registry, nil, nil)

	var exec executor.Executor
	switch os.Getenv("VERIPROC_EXECUTOR") {
	case "local":
		exec = executor.NewLocalExecutor(nil)
		logger.Info().Msg("executor: local (runs station scripts as OS processes)")
	default:
		exec = executor.NewStubExecutor(nil)
		logger.Info().Msg("executor: stub (simulates lifecycle without running scripts)")
	}
	groupSvc := groups.NewService(st, nil)
	runsSvc := runs.NewService(runs.Config{
		Store:           st,
		Executor:        exec,
		Resolver:        registry,
		WorkingRootBase: cfg.Paths.WorkingRootBase,
		RegisterGroup: func(ctx context.Context, splitGroupID, runID, taskID string) error {
			return groupSvc.RegisterRun(ctx, splitGroupID, runID, taskID, "")
		},
	})
	dispatcher := runs.NewDispatcher(runsSvc, 250*time.Millisecond, logger)
	reconcilerSvc := reconciler.New(reconciler.Config{
		Store:          st,
		Runs:           runsSvc,
		StaleThreshold: 60 * time.Second,
		Interval:       15 * time.Second,
		Logger:         logger,
	})

	// M6: parse VERIPROC_AUTH_TOKENS=subject:role:token[:quotaPerMin][;...]
	authn, quota := loadAuth(os.Getenv("VERIPROC_AUTH_TOKENS"))

	// M6: optional rolling-archive publisher.
	var pubSvc *publisher.Service
	if base := os.Getenv("VERIPROC_ARCHIVE_BASE"); base != "" {
		pubSvc = publisher.New(publisher.Config{
			Store:       st,
			ArchiveBase: base,
			Logger:      logger,
			Interval:    5 * time.Second,
		})
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Config: cfg,
		Health: agg,
		Logger: logger,
		Tasks:  taskSvc,
		Runs:   runsSvc,
		Groups: groupSvc,
		Authn:  authn,
		Quota:  quota,
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

	dispatcherCtx, cancelDispatcher := context.WithCancel(context.Background())
	defer cancelDispatcher()
	go func() {
		if err := dispatcher.Run(dispatcherCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn().Err(err).Msg("dispatcher exited with error")
		}
	}()

	// M6: idempotency-record sweeper. Runs hourly; safe to omit when no
	// idempotency records are written.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-dispatcherCtx.Done():
				return
			case <-t.C:
				if _, err := st.Idempotency().Sweep(dispatcherCtx, time.Now().UTC()); err != nil {
					logger.Warn().Err(err).Msg("idempotency sweep failed")
				}
			}
		}
	}()

	// M6: rolling-archive publisher.
	if pubSvc != nil {
		go func() {
			if err := pubSvc.Run(dispatcherCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn().Err(err).Msg("publisher exited with error")
			}
		}()
	}

	// M7: reconciliation worker.
	go reconcilerSvc.Run(dispatcherCtx)

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
	cancelDispatcher()

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

// loadAuth parses VERIPROC_AUTH_TOKENS into a StaticAuthenticator and
// QuotaEnforcer. The format is `subject:role:token[:quotaPerMin]` separated
// by `;`. Returns (nil, nil) when the spec is empty (open API).
func loadAuth(spec string) (auth.Authenticator, *auth.QuotaEnforcer) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	entries := map[string]auth.Principal{}
	for _, raw := range strings.Split(spec, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) < 3 {
			continue
		}
		p := auth.Principal{Subject: parts[0], Role: auth.Role(parts[1])}
		token := parts[2]
		if len(parts) >= 4 {
			var q int
			fmt.Sscanf(parts[3], "%d", &q)
			p.QuotaPerMinute = q
		}
		entries[token] = p
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return auth.NewStaticAuthenticator(entries), auth.NewQuotaEnforcer(time.Now)
}
