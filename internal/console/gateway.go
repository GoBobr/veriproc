package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Gateway is the top-level service object wiring config, persistence,
// upstream clients, and HTTP handlers together.
type Gateway struct {
	cfg       *Config
	db        *DB
	logger    zerolog.Logger
	instances map[string]*upstreamInstance
	now       func() time.Time
}

// upstreamInstance holds the per-instance runtime state.
type upstreamInstance struct {
	cfg    InstanceConfig
	client UpstreamClient
}

// Options bundles the constructor inputs for NewGateway.
type Options struct {
	Config    *Config
	DB        *DB
	Logger    zerolog.Logger
	// NewClient overrides the upstream HTTP client factory. Useful for tests.
	NewClient func(InstanceConfig) (UpstreamClient, error)
	// Now overrides the time source. Useful for tests.
	Now func() time.Time
}

// NewGateway constructs a Gateway. The supplied DB must already be migrated
// and the supplied Config must already be validated.
func NewGateway(opts Options) (*Gateway, error) {
	if opts.Config == nil {
		return nil, errors.New("console: missing config")
	}
	if opts.DB == nil {
		return nil, errors.New("console: missing db")
	}
	if opts.NewClient == nil {
		opts.NewClient = func(inst InstanceConfig) (UpstreamClient, error) {
			return NewHTTPUpstreamClient(inst)
		}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	g := &Gateway{
		cfg:       opts.Config,
		db:        opts.DB,
		logger:    opts.Logger,
		now:       opts.Now,
		instances: map[string]*upstreamInstance{},
	}
	for _, inst := range opts.Config.Instances {
		cl, err := opts.NewClient(inst)
		if err != nil {
			return nil, fmt.Errorf("console: build upstream client for %s: %w", inst.ID, err)
		}
		g.instances[inst.ID] = &upstreamInstance{cfg: inst, client: cl}
	}
	return g, nil
}

// Config returns the gateway configuration.
func (g *Gateway) Config() *Config { return g.cfg }

// DB returns the gateway database handle.
func (g *Gateway) DB() *DB { return g.db }

// Instances returns the configured instance IDs in their declared order.
func (g *Gateway) Instances() []InstanceConfig {
	out := make([]InstanceConfig, 0, len(g.cfg.Instances))
	for _, inst := range g.cfg.Instances {
		out = append(out, inst)
	}
	return out
}

// instance returns the named upstream wrapper.
func (g *Gateway) instance(id string) (*upstreamInstance, bool) {
	inst, ok := g.instances[id]
	return inst, ok
}

// SeedTokens registers the inline tokens from the config into the console DB.
// Existing rows for the same token hash are overwritten so config changes
// propagate on restart.
func (g *Gateway) SeedTokens(ctx context.Context) error {
	for _, t := range g.cfg.Auth.Tokens {
		role, err := NormalizeRole(t.Role)
		if err != nil {
			return fmt.Errorf("console: token %q: %w", t.Subject, err)
		}
		if t.Token == "" {
			continue
		}
		if err := g.db.RegisterToken(ctx, HashToken(t.Token), t.Subject, string(role), g.now()); err != nil {
			return fmt.Errorf("console: register token %s: %w", t.Subject, err)
		}
	}
	return nil
}

// resolveUpstreamRunID maps (task_id, retry_index) to an upstream run_id.
func (g *Gateway) resolveUpstreamRunID(ctx context.Context, instanceID, taskID string, retryIndex int) (string, error) {
	inst, ok := g.instance(instanceID)
	if !ok {
		return "", errors.New("console: unknown instance")
	}
	run, err := ResolveRunByTaskAndRetry(ctx, inst.client, taskID, retryIndex)
	if err != nil {
		return "", err
	}
	return RunIDOf(run), nil
}

// audit records a mutating action without blocking the response. Errors are
// only logged.
func (g *Gateway) audit(ctx context.Context, e AuditEntry) {
	if e.At.IsZero() {
		e.At = g.now()
	}
	if err := g.db.RecordAudit(ctx, e); err != nil {
		g.logger.Warn().Err(err).Str("subject", e.Subject).Str("action", e.Action).Msg("console: audit log failed")
	}
}

// httpStatusFromUpstream maps an upstream error onto a sensible console status.
func httpStatusFromUpstream(err error) int {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		// Pass through client errors verbatim; otherwise surface 502 to
		// distinguish upstream failures from gateway bugs.
		if ue.Status >= 400 && ue.Status < 500 {
			return ue.Status
		}
		return http.StatusBadGateway
	}
	return http.StatusBadGateway
}
