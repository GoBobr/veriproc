package executor

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoExecutor is returned by Registry.Resolve when the requested executor
// type is not configured at the instance level.
var ErrNoExecutor = errors.New("executor: type not configured")

// Registry maps executor type names to Executor instances. Stations name the
// executor they want via execution.mode; the registry looks it up.
//
// An optional default type is used when the station declares no mode. If no
// default is set and the station declares no mode, Resolve returns ErrNoExecutor.
type Registry struct {
	executors  map[string]Executor
	defaultKey string
}

// NewRegistry constructs a Registry from a pre-built map of Executor instances.
// defaultKey is the type name used when Resolve is called with an empty mode;
// pass an empty string to require explicit executor selection by every station.
func NewRegistry(executors map[string]Executor, defaultKey string) *Registry {
	return &Registry{executors: executors, defaultKey: defaultKey}
}

// Resolve returns the Executor for the given mode. If mode is empty the
// instance default is used. Returns a descriptive ErrNoExecutor if the type
// is not in the registry.
func (r *Registry) Resolve(mode string) (Executor, error) {
	key := strings.TrimSpace(strings.ToLower(mode))
	if key == "" {
		if r.defaultKey == "" {
			return nil, fmt.Errorf("%w: station declared no execution.mode and no default executor is "+
				"configured (set executor.default in instance.yaml)", ErrNoExecutor)
		}
		key = r.defaultKey
	}
	e, ok := r.executors[key]
	if !ok {
		available := make([]string, 0, len(r.executors))
		for k := range r.executors {
			available = append(available, k)
		}
		sort.Strings(available)
		return nil, fmt.Errorf("%w: station requested executor type %q but it is not configured "+
			"in executor.executors (available: %s); add it to instance.yaml",
			ErrNoExecutor, key, strings.Join(available, ", "))
	}
	return e, nil
}

// DefaultKey returns the configured default executor type name (empty if none).
func (r *Registry) DefaultKey() string { return r.defaultKey }

// NewSingleExecutorRegistry wraps one Executor in a Registry, using its
// Type() as both the sole registry key and the default. Convenient for tests
// and for the legacy single-executor config path.
func NewSingleExecutorRegistry(e Executor) *Registry {
	return NewRegistry(map[string]Executor{e.Type(): e}, e.Type())
}
