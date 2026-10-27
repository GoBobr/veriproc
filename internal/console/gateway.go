package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/accessprofile"
)

// Gateway is the top-level service object wiring config, persistence,
// upstream clients, and HTTP handlers together.
type Gateway struct {
	cfg       *Config
	db        *DB
	logger    zerolog.Logger
	instances map[string]*upstreamInstance
	now       func() time.Time
	// profile is the shared access-profile source used to authenticate
	// console users. Nil when no profile is configured, in which case the
	// legacy console-local token table applies.
	profile *accessprofile.Source
}

// upstreamInstance holds the per-instance runtime state.
type upstreamInstance struct {
	cfg    InstanceConfig
	client UpstreamClient

	// accessMu guards accessDenied, the outcome of the most recent upstream
	// probe with respect to authentication/authorization.
	accessMu sync.Mutex
	// accessDenied is set when the last upstream probe was rejected with 401
	// or 403, meaning the gateway's own credential cannot reach that
	// instance. It is cleared by a subsequent successful probe.
	accessDenied bool
}

// observeUpstream records whether err represents an upstream authentication
// or authorization rejection for inst. A nil error clears the flag.
func (u *upstreamInstance) observeUpstream(err error) {
	denied := err != nil && (IsUpstreamStatus(err, http.StatusUnauthorized) ||
		IsUpstreamStatus(err, http.StatusForbidden))
	u.accessMu.Lock()
	u.accessDenied = denied
	u.accessMu.Unlock()
}

// accessible reports whether the gateway's credential is believed to reach
// this instance. Instances are accessible until proven otherwise.
func (u *upstreamInstance) accessible() bool {
	u.accessMu.Lock()
	defer u.accessMu.Unlock()
	return !u.accessDenied
}

// reprobeInstances probes every configured instance's health and records the
// access outcome, so the visible-instance set reflects the current upstream
// state. It is called after a successful identity resolution (login) so that
// a stronger credential's instances become visible without a restart.
func (g *Gateway) reprobeInstances(ctx context.Context) {
	var wg sync.WaitGroup
	for _, inst := range g.instances {
		wg.Add(1)
		go func(u *upstreamInstance) {
			defer wg.Done()
			u.observeUpstream(u.client.Health(ctx))
		}(inst)
	}
	wg.Wait()
}

// Options bundles the constructor inputs for NewGateway.
type Options struct {
	Config *Config
	DB     *DB
	Logger zerolog.Logger
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
		slotCap := 12
		if opts.Config.UI.VisibleSlotCount > 0 {
			slotCap = opts.Config.UI.VisibleSlotCount
		}
		opts.NewClient = func(inst InstanceConfig) (UpstreamClient, error) {
			cl, err := NewHTTPUpstreamClient(inst)
			if err != nil {
				return nil, err
			}
			cl.slotCap = slotCap
			return cl, nil
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
	if opts.Config.Auth.ProfilePath != "" {
		src, err := accessprofile.NewSource(opts.Config.Auth.ProfilePath, accessprofile.Options{
			Warn: func(e error) { opts.Logger.Warn().Err(e).Msg("access profile") },
		})
		if err != nil {
			return nil, fmt.Errorf("console: load access profile: %w", err)
		}
		g.profile = src
		opts.Logger.Info().
			Str("profile", src.Path()).
			Bool("anonymous_browsing", opts.Config.Auth.AllowAnonymousBrowsing).
			Msg("access profile loaded")
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
//
// It is a no-op when an access profile is configured: the profile is then the
// sole credential source, so the console-local token table stops being an
// authority for authenticating users.
func (g *Gateway) SeedTokens(ctx context.Context) error {
	if g.profile != nil {
		return nil
	}
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

// audit records a mutating action in the persistent audit log and emits a
// structured log line so operators see every user action in the console log.
func (g *Gateway) audit(ctx context.Context, e AuditEntry) {
	if e.At.IsZero() {
		e.At = g.now()
	}

	// Structured log — always at Info so user actions are visible at the
	// default log level without needing to set debug.
	ev := g.logger.Info().
		Str("subject", e.Subject).
		Str("action", e.Action).
		Str("status", e.Status).
		Str("instance", e.InstanceID)
	if e.StationID != "" {
		ev = ev.Str("station", e.StationID)
	}
	if e.TaskID != "" {
		ev = ev.Str("task", e.TaskID)
	}
	if e.RetryIndex != nil {
		ev = ev.Int("retry", *e.RetryIndex)
	}
	if e.Message != "" {
		ev = ev.Str("detail", e.Message)
	}
	ev.Msg("console_action")

	// Persistent audit record — errors only logged, never bubble up.
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
