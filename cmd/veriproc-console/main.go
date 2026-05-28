// Command veriproc-console runs the operator web-console gateway described
// in Spec §8. It is independent of veriprocd: a single console instance can
// front several upstream veriprocd deployments.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/console"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "veriproc-console: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("veriproc-console", flag.ContinueOnError)
	var (
		cfgPath   string
		bindAddr  string
		webappDir string
		logLevel  string
	)
	flags.StringVar(&cfgPath, "config", os.Getenv("VERIPROC_CONSOLE_CONFIG"), "path to console gateway YAML config")
	flags.StringVar(&bindAddr, "http-addr", "", "override http.bind_addr")
	flags.StringVar(&webappDir, "webapp-dir", os.Getenv("VERIPROC_CONSOLE_WEBAPP"), "directory containing built frontend (overrides config)")
	flags.StringVar(&logLevel, "log-level", "info", "log level (trace, debug, info, warn, error)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if cfgPath == "" {
		return errors.New("--config is required (or set VERIPROC_CONSOLE_CONFIG)")
	}

	cfg, err := console.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	if bindAddr != "" {
		cfg.HTTP.BindAddr = bindAddr
	}
	if webappDir != "" {
		cfg.WebappDir = webappDir
	}

	level, err := zerolog.ParseLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).Level(level).With().Timestamp().Logger()

	db, err := console.OpenDB(cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		return err
	}

	gw, err := console.NewGateway(console.Options{Config: cfg, DB: db, Logger: logger})
	if err != nil {
		return err
	}
	if err := gw.SeedTokens(context.Background()); err != nil {
		return err
	}

	var webappFS iofs.FS
	if cfg.WebappDir != "" {
		abs, err := filepath.Abs(cfg.WebappDir)
		if err != nil {
			return err
		}
		webappFS = os.DirFS(abs)
		logger.Info().Str("webapp_dir", abs).Msg("serving frontend from directory")
	} else {
		logger.Warn().Msg("no webapp_dir configured; only /api/console endpoints are served")
	}

	handler := gw.Router(webappFS)
	srv := &http.Server{
		Addr:         cfg.HTTP.BindAddr,
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	logger.Info().Str("bind_addr", cfg.HTTP.BindAddr).Int("instances", len(cfg.Instances)).Msg("console gateway starting")

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
		logger.Info().Str("signal", sig.String()).Msg("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info().Msg("console gateway stopped cleanly")
	return nil
}

// ensure strings imported even if unused in some builds.
var _ = strings.Contains
