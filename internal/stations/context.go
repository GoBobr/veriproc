package stations

import (
	"fmt"
	"regexp"
	"strings"
)

// contextRefPattern matches station context references of the form <name> or
// <name.path>. Only references whose first segment starts with a lowercase
// letter or underscore are treated as context references; this avoids
// consuming structured filename placeholders like <MISSION_ID> which start
// with an uppercase letter.
//
// Each captured group is the full dot-path (e.g. "facility.processing_center").
var contextRefPattern = regexp.MustCompile(`<([a-z_][a-zA-Z0-9_]*)(?:\.[a-zA-Z0-9_.]+)?>`)

// fullRefPattern matches a string that is exactly one context reference (no
// surrounding text). Full references may resolve to any type.
var fullRefPattern = regexp.MustCompile(`^<([a-z_][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_.]+)?)>$`)

// ResolveString resolves context references within a single scalar string
// value.
//
//   - If s is exactly one reference (<name> or <name.path>), the resolved
//     value is returned preserving its native type (scalar, map, slice, nil).
//   - If s contains references embedded in a larger string, all references must
//     resolve to scalar values; they are converted to strings and interpolated.
//   - Unresolved references or type-incompatible substitutions return an error.
func ResolveString(s string, ctx map[string]any) (any, error) {
	// Fast path: no angle brackets.
	if !strings.ContainsRune(s, '<') {
		return s, nil
	}

	// Full-reference: entire value is one reference.
	if m := fullRefPattern.FindStringSubmatch(s); m != nil {
		v, err := lookupPath(ctx, m[1])
		if err != nil {
			return nil, fmt.Errorf("context reference <%s>: %w", m[1], err)
		}
		return v, nil
	}

	// Embedded references: must all resolve to scalars.
	var resolveErr error
	result := contextRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		if resolveErr != nil {
			return match
		}
		// Extract the path from inside the angle brackets.
		inner := match[1 : len(match)-1]
		v, err := lookupPath(ctx, inner)
		if err != nil {
			resolveErr = fmt.Errorf("context reference <%s>: %w", inner, err)
			return match
		}
		sv, ok := scalarString(v)
		if !ok {
			resolveErr = fmt.Errorf("context reference <%s>: embedded reference must resolve to a scalar, got %T", inner, v)
			return match
		}
		return sv
	})
	if resolveErr != nil {
		return nil, resolveErr
	}
	return result, nil
}

// ResolveMap resolves context references in all scalar string leaves of m,
// returning a new map with resolved values. The original map is not modified.
func ResolveMap(m map[string]any, ctx map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		resolved, err := resolveValue(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = resolved
	}
	return out, nil
}

// ResolveArgs resolves context references in each element of args, returning a
// new slice. Every resolved value must be a scalar; a full-reference that
// resolves to a non-scalar is an error.
func ResolveArgs(args []string, ctx map[string]any) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := ResolveString(a, ctx)
		if err != nil {
			return nil, fmt.Errorf("arg[%d] %q: %w", i, a, err)
		}
		sv, ok := scalarString(v)
		if !ok {
			return nil, fmt.Errorf("arg[%d] %q: resolved to non-scalar type %T", i, a, v)
		}
		out[i] = sv
	}
	return out, nil
}

// lookupPath traverses ctx following the dot-separated segments of path.
func lookupPath(ctx map[string]any, path string) (any, error) {
	segments := strings.Split(path, ".")
	var cur any = ctx
	for i, seg := range segments {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot traverse into non-mapping value at segment %q (path %q)",
				strings.Join(segments[:i], "."), path)
		}
		v, exists := m[seg]
		if !exists {
			return nil, fmt.Errorf("key %q not found (path %q)", seg, path)
		}
		cur = v
	}
	return cur, nil
}

// scalarString converts a value to its string representation if it is a
// scalar (string, int, float, bool, nil). Returns ("", false) for non-scalars.
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return fmt.Sprintf("%d", t), true
	case int64:
		return fmt.Sprintf("%d", t), true
	case float64:
		return fmt.Sprintf("%g", t), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case nil:
		return "", true
	}
	return "", false
}

// resolveValue recursively resolves context references within v.
func resolveValue(v any, ctx map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		return ResolveString(t, ctx)
	case map[string]any:
		return ResolveMap(t, ctx)
	case []any:
		out := make([]any, len(t))
		for i, elem := range t {
			resolved, err := resolveValue(elem, ctx)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = resolved
		}
		return out, nil
	default:
		// Scalars without string representation (int, bool, nil, etc.) pass through.
		return v, nil
	}
}
