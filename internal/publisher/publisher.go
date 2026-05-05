// Package publisher implements rolling-archive publication: it copies
// canonical artifacts from each run's working root to a configured archive
// base path and updates publication state.
//
// Spec references: §2.5.6, §4.3.12, §4.6.6, §5.5.6.
package publisher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/store"
)

// Service publishes pending publications by copying their source artifact
// bytes into the configured archive base directory.
type Service struct {
	store       *store.Store
	archiveBase string
	clock       func() time.Time
	log         zerolog.Logger
	interval    time.Duration
	batchSize   int
}

// Config configures a publisher Service.
type Config struct {
	Store       *store.Store
	ArchiveBase string        // root directory under which archive files are written
	Clock       func() time.Time
	Logger      zerolog.Logger
	Interval    time.Duration // poll interval; default 5s
	BatchSize   int           // max publications per tick; default 25
}

// New constructs a Service.
func New(cfg Config) *Service {
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 25
	}
	return &Service{
		store:       cfg.Store,
		archiveBase: cfg.ArchiveBase,
		clock:       cfg.Clock,
		log:         cfg.Logger,
		interval:    cfg.Interval,
		batchSize:   cfg.BatchSize,
	}
}

// Run blocks until ctx is cancelled, periodically calling Tick.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn().Err(err).Msg("publisher tick failed")
			}
		}
	}
}

// Tick processes one batch of pending publications. Returns the number of
// publications that were transitioned to a terminal state (published or
// failed).
func (s *Service) Tick(ctx context.Context) (int, error) {
	pending, err := s.store.Publications().ListPending(ctx, s.batchSize)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, p := range pending {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if err := s.publishOne(ctx, p); err != nil {
			s.log.Warn().Err(err).Str("publication_id", p.PublicationID).Msg("publication failed")
			_ = s.store.Publications().MarkFailed(ctx, p.PublicationID, err.Error())
		}
		processed++
	}
	return processed, nil
}

func (s *Service) publishOne(ctx context.Context, p *store.PublicationRecord) error {
	art, err := s.store.Artifacts().Get(ctx, p.ArtifactID)
	if err != nil {
		return fmt.Errorf("load artifact: %w", err)
	}
	if art.Path == "" {
		return errors.New("source artifact has no path")
	}
	src, err := os.Open(art.Path)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	dst := filepath.Join(s.archiveBase, p.TargetPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create target: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close target: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename target: %w", err)
	}
	now := s.clock().UTC()
	return s.store.Publications().MarkPublished(ctx, p.PublicationID, now)
}
