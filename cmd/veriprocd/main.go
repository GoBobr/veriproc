// Command veriprocd runs the VeriProc backend HTTP server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"

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
		Str("commit", version.Commit).
		Str("build_date", version.BuildDate).
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
	if cfg.Storage.StationConfigRoot != "" {
		specs, err := stations.LoadDir(context.Background(), cfg.Storage.StationConfigRoot, registry, st)
		if err != nil {
			return fmt.Errorf("load stations: %w", err)
		}
		logger.Info().Str("dir", cfg.Storage.StationConfigRoot).Int("count", len(specs)).Msg("loaded stations")
	}
	taskSvc := tasks.NewService(st, registry, nil, nil)
	taskSvc.SetNaming(cfg.Naming)
	taskSvc.SetLogger(logger)

	execRegistry, err := buildExecutorRegistry(cfg.Executor, logger)
	if err != nil {
		return err
	}
	groupSvc := groups.NewService(st, nil)
	stationSvc := stations.NewService(st, nil)
	archivePaths := map[string]string{}
	for id, archive := range cfg.RollingArchives {
		archivePaths[id] = archive.Path
	}
	productCategories := map[string][]string{}
	for _, cat := range cfg.ProductCategories {
		productCategories[cat.Name] = append([]string(nil), cat.Folders...)
	}
	generators := map[string]string{}
	jobOrderPaths := "relative"
	for name, gen := range cfg.Generators {
		generators[name] = gen.Version
		if name == "job_order" && gen.Paths != "" {
			jobOrderPaths = gen.Paths
		}
	}
	runsSvc := runs.NewService(runs.Config{
		Store:             st,
		Executors:         execRegistry,
		Resolver:          registry,
		WorkingRootBase:   cfg.Storage.WorkingRootBase,
		Naming:            cfg.Naming,
		Integrity:         cfg.Integrity,
		InstanceID:        cfg.InstanceID,
		Facility:          cfg.Facility,
		Definitions:       cfg.Definitions,
		RollingArchives:   archivePaths,
		ProductCategories: productCategories,
		Generators:        generators,
		JobOrderPaths:     jobOrderPaths,
		Logger:            logger,
		RegisterGroup: func(ctx context.Context, splitGroupID, runID, taskID string) error {
			return groupSvc.RegisterRun(ctx, splitGroupID, runID, taskID, "")
		},
		NotifyGroupComplete: buildGroupCompleteNotifier(st, taskSvc, groupSvc, registry, logger),
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
	if base := firstArchiveBase(archivePaths); base != "" {
		pubSvc = publisher.New(publisher.Config{
			Store:       st,
			ArchiveBase: base,
			Logger:      logger,
			Interval:    5 * time.Second,
		})
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Config:   cfg,
		Health:   agg,
		Logger:   logger,
		Tasks:    taskSvc,
		Runs:     runsSvc,
		Stations: stationSvc,
		Groups:   groupSvc,
		Authn:    authn,
		Quota:    quota,
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

func buildExecutorRegistry(cfg config.ExecutorConfig, logger zerolog.Logger) (*executor.Registry, error) {
	// Multi-executor format: executor.executors is populated.
	if len(cfg.Executors) > 0 {
		execs := make(map[string]executor.Executor, len(cfg.Executors))
		for typeName, spec := range cfg.Executors {
			switch typeName {
			case "stub":
				execs[typeName] = executor.NewStubExecutor(nil)
				logger.Info().Str("type", typeName).Msg("executor registered")
			case "local":
				execs[typeName] = executor.NewLocalExecutor(nil)
				logger.Info().Str("type", typeName).Msg("executor registered")
			case executor.SlurmNative, executor.SlurmDocker:
				execs[typeName] = executor.NewSlurmExecutor(slurmSpecConfig(typeName, spec))
				logger.Info().Str("type", typeName).Msg("executor registered")
			}
		}
		return executor.NewRegistry(execs, cfg.Default), nil
	}

	// Legacy single-executor format.
	execType := strings.TrimSpace(strings.ToLower(cfg.Type))
	if execType == "" {
		execType = strings.TrimSpace(strings.ToLower(os.Getenv("VERIPROC_EXECUTOR")))
	}
	if execType == "" {
		execType = "stub"
	}
	var exec executor.Executor
	switch execType {
	case "stub":
		exec = executor.NewStubExecutor(nil)
		logger.Info().Msg("executor: stub (simulates lifecycle without running scripts)")
	case "local":
		exec = executor.NewLocalExecutor(nil)
		logger.Info().Msg("executor: local (runs station scripts as OS processes)")
	case executor.SlurmNative, executor.SlurmDocker:
		exec = executor.NewSlurmExecutor(slurmExecutorConfig(cfg))
		logger.Info().Str("type", execType).Msg("executor: slurm")
	default:
		return nil, fmt.Errorf("executor: unsupported type %q", execType)
	}
	return executor.NewSingleExecutorRegistry(exec), nil
}

func slurmSpecConfig(typeName string, spec config.ExecutorSpec) executor.SlurmConfig {
	return executor.SlurmConfig{
		Type: typeName,
		Connection: executor.SlurmConnection{
			Mode:    spec.Slurm.Connection.Mode,
			Host:    spec.Slurm.Connection.Host,
			User:    spec.Slurm.Connection.User,
			KeyFile: spec.Slurm.Connection.KeyFile,
		},
		Account:       spec.Slurm.Account,
		Partition:     spec.Slurm.Partition,
		QOS:           spec.Slurm.QOS,
		SubmitCommand: spec.Slurm.SubmitCommand,
		QueryCommand:  spec.Slurm.QueryCommand,
		CancelCommand: spec.Slurm.CancelCommand,
		Defaults: executor.ResourceRequest{
			CPUsPerTask: spec.Slurm.Defaults.CPUsPerTask,
			MemGB:       spec.Slurm.Defaults.MemGB,
			Walltime:    spec.Slurm.Defaults.Walltime,
		},
		Docker: executor.DockerDefaults{
			DefaultMounts: append([]string(nil), spec.Docker.DefaultMounts...),
			User:          spec.Docker.User,
		},
	}
}

func slurmExecutorConfig(cfg config.ExecutorConfig) executor.SlurmConfig {
	return executor.SlurmConfig{
		Type: cfg.Type,
		Connection: executor.SlurmConnection{
			Mode:    cfg.Slurm.Connection.Mode,
			Host:    cfg.Slurm.Connection.Host,
			User:    cfg.Slurm.Connection.User,
			KeyFile: cfg.Slurm.Connection.KeyFile,
		},
		Account:       cfg.Slurm.Account,
		Partition:     cfg.Slurm.Partition,
		QOS:           cfg.Slurm.QOS,
		SubmitCommand: cfg.Slurm.SubmitCommand,
		QueryCommand:  cfg.Slurm.QueryCommand,
		CancelCommand: cfg.Slurm.CancelCommand,
		Defaults: executor.ResourceRequest{
			CPUsPerTask: cfg.Slurm.Defaults.CPUsPerTask,
			MemGB:       cfg.Slurm.Defaults.MemGB,
			Walltime:    cfg.Slurm.Defaults.Walltime,
		},
		Docker: executor.DockerDefaults{
			DefaultMounts: append([]string(nil), cfg.Docker.DefaultMounts...),
			User:          cfg.Docker.User,
		},
	}
}

func firstArchiveBase(paths map[string]string) string {
	if paths["default"] != "" {
		return paths["default"]
	}
	for _, path := range paths {
		return path
	}
	return ""
}

// buildGroupCompleteNotifier returns a GroupCompleteNotifier that, when a
// split group transitions to "complete", submits the aggregation target with
// the parent task's window. Group IDs follow the sandbox convention
// "sg-<parentTaskID>".
func buildGroupCompleteNotifier(
	st *store.Store,
	taskSvc *tasks.Service,
	groupSvc *groups.Service,
	resolver stations.Resolver,
	log zerolog.Logger,
) runs.GroupCompleteNotifier {
	return func(ctx context.Context, groupID string) {
		derived, err := groupSvc.Aggregate(ctx, groupID)
		if err != nil {
			log.Warn().Err(err).Str("group_id", groupID).Msg("group-complete notifier: aggregate failed")
			return
		}
		if derived != "complete" {
			return // not complete yet; another canonical run will trigger us again
		}

		// Derive parent task ID from group ID (convention: "sg-<parentTaskID>").
		if len(groupID) <= 3 || groupID[:3] != "sg-" {
			log.Warn().Str("group_id", groupID).Msg("group-complete notifier: unexpected group_id format, cannot derive parent task")
			return
		}
		parentTaskID := groupID[3:]

		parentTask, err := st.Tasks().Get(ctx, parentTaskID)
		if err != nil {
			log.Warn().Err(err).Str("parent_task_id", parentTaskID).Msg("group-complete notifier: parent task not found")
			return
		}

		targets, err := fanInTargetsForGroup(ctx, st, resolver, parentTask, groupID)
		if err != nil {
			log.Warn().Err(err).Str("group_id", groupID).Msg("group-complete notifier: resolve fan-in targets")
			return
		}
		if len(targets) == 0 {
			return
		}

		idempotencyBase := "fan-in-" + groupID

		// Build the routing history chain for the fan-in task so that
		// task.yaml carries the full ancestral chain: ancestors of the
		// fan-out parent, the fan-out parent itself, and the member (statD)
		// runs that belong to this split group.
		var fanInHistory []any
		var parentRunRef string

		// 1. Ancestors inherited from the fan-out parent's routing content.
		if len(parentTask.RoutingContent) > 0 {
			var rc map[string]any
			if jerr := json.Unmarshal(parentTask.RoutingContent, &rc); jerr == nil {
				if h, ok := rc["history"].([]any); ok {
					fanInHistory = h
				}
			}
		}

		// 2. Fan-out parent (statC) entry.
		if parentTask.CanonicalRunID != "" {
			if canonRun, rerr := st.Runs().Get(ctx, parentTask.CanonicalRunID); rerr == nil {
				parentRunRef = fmt.Sprintf("%s/r%d", parentTask.TaskID, canonRun.RetryIndex)
				completedAt := ""
				if canonRun.TerminalAt.Valid {
					completedAt = canonRun.TerminalAt.Time.UTC().Format("2006-01-02T15:04:05.000Z07:00")
				}
				entry := map[string]any{
					"station_id": parentTask.DestinationStationID,
					"run_ref":    parentRunRef,
				}
				if completedAt != "" {
					entry["completed_at"] = completedAt
				}
				if rev, rerr := resolver.Resolve(ctx, parentTask.DestinationStationID); rerr == nil {
					entry["summary"] = "station " + rev.StationName + " completed"
				}
				fanInHistory = append(fanInHistory, entry)
			}
		}

		// 3. Member runs (statD) that belong to this split group, ordered
		//    by window start so the history reads chronologically.
		if members, merr := st.SplitGroups().ListMembers(ctx, groupID); merr == nil {
			type memberEntry struct {
				windowStart time.Time
				entry       map[string]any
			}
			var memberEntries []memberEntry
			for _, m := range members {
				memberRun, rerr := st.Runs().Get(ctx, m.RunID)
				if rerr != nil {
					continue
				}
				memberTask, terr := st.Tasks().Get(ctx, m.TaskID)
				if terr != nil {
					continue
				}
				runRef := fmt.Sprintf("%s/r%d", m.TaskID, memberRun.RetryIndex)
				e := map[string]any{
					"station_id":   memberTask.DestinationStationID,
					"run_ref":      runRef,
					"canonicality": memberRun.Canonicality,
				}
				if memberRun.TerminalAt.Valid {
					e["completed_at"] = memberRun.TerminalAt.Time.UTC().Format("2006-01-02T15:04:05.000Z07:00")
				}
				memberEntries = append(memberEntries, memberEntry{windowStart: memberTask.WindowStart, entry: e})
			}
			// Sort by window start for a chronological order.
			sort.Slice(memberEntries, func(i, j int) bool {
				return memberEntries[i].windowStart.Before(memberEntries[j].windowStart)
			})
			for _, me := range memberEntries {
				fanInHistory = append(fanInHistory, me.entry)
			}
		}

		for _, stationID := range targets {
			result, serr := taskSvc.Submit(ctx, tasks.SubmitInput{
				IdempotencyKey: idempotencyBase + "-" + stationID,
				Destination:    tasks.Destination{StationID: stationID},
				Window: tasks.Window{
					Start: parentTask.WindowStart,
					End:   parentTask.WindowEnd,
				},
				Parent: &tasks.Parent{
					TaskID: parentTaskID,
					RunRef: parentRunRef,
				},
				History:   fanInHistory,
				TriggerID: "group:" + groupID,
			})
			if serr != nil {
				log.Warn().Err(serr).Str("station_id", stationID).Msg("group-complete notifier: submit fan-in failed")
				continue
			}
			if result.Created {
				log.Info().Str("group_id", groupID).Str("fan_in_station", stationID).Str("task_id", result.Task.TaskID).Msg("group-complete notifier: fan-in task submitted")
			} else {
				log.Debug().Str("group_id", groupID).Str("fan_in_station", stationID).Msg("group-complete notifier: fan-in task already exists (idempotent)")
			}
		}
	}
}

type daemonTaskOutDescriptor struct {
	SplitGroups []daemonTaskOutSplitGroup `yaml:"split_groups"`
}

type daemonTaskOutSplitGroup struct {
	GroupID              string `yaml:"group_id"`
	AggregationStationID string `yaml:"aggregation_station_id"`
}

func fanInTargetsForGroup(ctx context.Context, st *store.Store, resolver stations.Resolver, parentTask *store.TaskRecord, groupID string) ([]string, error) {
	if parentTask.CanonicalRunID != "" {
		run, err := st.Runs().Get(ctx, parentTask.CanonicalRunID)
		if err == nil {
			if targets, ok, err := taskOutFanInTargets(run.WorkingRoot, groupID); err != nil {
				return nil, err
			} else if ok {
				for _, stationID := range targets {
					if _, err := resolver.Resolve(ctx, stationID); err != nil {
						return nil, err
					}
				}
				return targets, nil
			}
		}
	}

	// Fallback for station-default grouped workflows that do not emit task-out.yaml.
	rev, err := resolver.Resolve(ctx, parentTask.DestinationStationID)
	if err != nil || rev.DeclaredDownstream == "" {
		return nil, err
	}
	var routes []stations.DownstreamTarget
	if err := json.Unmarshal([]byte(rev.DeclaredDownstream), &routes); err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(routes))
	for _, route := range routes {
		if route.Mode == "fan_in" {
			targets = append(targets, route.StationID)
		}
	}
	return targets, nil
}

func taskOutFanInTargets(workingRoot, groupID string) ([]string, bool, error) {
	paths := []string{
		filepath.Join(workingRoot, "task-out.yaml"),
		filepath.Join(workingRoot, "output", "task-out.yaml"),
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false, err
		}
		var desc daemonTaskOutDescriptor
		if err := yaml.Unmarshal(b, &desc); err != nil {
			return nil, false, err
		}
		for _, group := range desc.SplitGroups {
			if strings.TrimSpace(group.GroupID) == groupID && strings.TrimSpace(group.AggregationStationID) != "" {
				return []string{strings.TrimSpace(group.AggregationStationID)}, true, nil
			}
		}
	}
	return nil, false, nil
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
