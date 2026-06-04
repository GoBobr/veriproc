// Package envexpand provides strict environment-variable expansion for
// configuration file content.
//
// Syntax: <env:VAR_NAME>
//
// This syntax was deliberately chosen to integrate with the existing station
// context-reference syntax (<key>, <key.subkey>) and to avoid any conflict
// with shell variable notation ($VAR, ${VAR}) that must pass through
// unchanged into job orders and runner scripts executed at run time.
//
// Design:
//   - Only tokens matching <env:VAR_NAME> are substituted; everything else
//     (including $VAR, ${VAR}, $$, and plain <key> references) is left
//     verbatim.
//   - VAR_NAME must be a valid shell identifier: [A-Za-z_][A-Za-z0-9_]*.
//   - Any referenced variable that is absent from the supplied map causes an
//     error, enabling fail-fast at daemon startup.
//   - Passing a nil map skips expansion entirely, so unit tests that write
//     inline YAML without <env:…> tokens need not supply an env map.
package envexpand

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// envToken matches <env:VALID_SHELL_NAME>.
var envToken = regexp.MustCompile(`<env:([A-Za-z_][A-Za-z0-9_]*)>`)

// Strict replaces every <env:VAR_NAME> token in s with the corresponding
// value from env.
//
// If env is nil, s is returned unchanged.
// Returns an error listing every variable name that is absent from env.
func Strict(s string, env map[string]string) (string, error) {
	if env == nil {
		return s, nil
	}
	var undefined []string
	result := envToken.ReplaceAllStringFunc(s, func(match string) string {
		// Extract the name from the match, e.g. "<env:FOO>" → "FOO".
		name := match[5 : len(match)-1]
		if v, ok := env[name]; ok {
			return v
		}
		undefined = append(undefined, name)
		return "" // value unused when undefined is non-empty
	})
	if len(undefined) > 0 {
		sort.Strings(undefined)
		return "", fmt.Errorf("undefined environment variable(s): %s",
			strings.Join(undefined, ", "))
	}
	return result, nil
}
