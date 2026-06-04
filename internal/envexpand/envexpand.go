// Package envexpand provides strict environment-variable expansion for
// configuration file content.
//
// Syntax: ${VAR} or $VAR — identical to what os.Expand recognises.
// $$ is an escape sequence that produces a literal $.
//
// Design choice:
//   - Expansion is applied to the raw YAML/text bytes before parsing so that
//     any YAML value, key, or path can reference an environment variable.
//   - Any referenced variable that is absent from the supplied map causes an
//     error, enabling "fail fast" at daemon startup rather than silently using
//     an empty value.
//   - Passing a nil map skips expansion entirely (all $… references are left
//     as-is). This is used by unit tests that write inline YAML without
//     ${VAR} references.
package envexpand

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Strict expands ${VAR} and $VAR references in s using the supplied env map.
//
// If env is nil, s is returned unchanged.
// Returns an error listing every variable name that is absent from env.
func Strict(s string, env map[string]string) (string, error) {
	if env == nil {
		return s, nil
	}
	var undefined []string
	expanded := os.Expand(s, func(key string) string {
		if v, ok := env[key]; ok {
			return v
		}
		undefined = append(undefined, key)
		return "" // value unused when undefined is non-empty
	})
	if len(undefined) > 0 {
		sort.Strings(undefined)
		return "", fmt.Errorf("undefined environment variable(s): %s",
			strings.Join(undefined, ", "))
	}
	return expanded, nil
}
