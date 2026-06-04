package stations

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// contextRefPattern matches station context references of the form <name>,
// <name.path>, or <name.path:type>. Only references whose first segment starts
// with a lowercase letter or underscore are treated as context references; this
// avoids consuming structured filename placeholders like <MISSION_ID> which
// start with an uppercase letter. An optional ":type" suffix (e.g. ":int")
// may appear before the closing bracket to request a type cast.
var contextRefPattern = regexp.MustCompile(`<([a-z_][a-zA-Z0-9_]*)(?:\.[a-zA-Z0-9_.]+)?(?::[a-z]+)?>`)

// fullRefPattern matches a string that is exactly one context reference (no
// surrounding text). Full references may resolve to any type. Group 1 is the
// dot-path; group 2 (may be empty) is the optional type-cast name.
var fullRefPattern = regexp.MustCompile(`^<([a-z_][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_.]+)?)(?::([a-z]+))?>$`)

// ResolveString resolves context references within a single scalar string
// value.
//
//   - If s is exactly one reference (<name>, <name.path>, or <name.path:type>),
//     the resolved value is returned preserving its native type (scalar, map,
//     slice, nil). An optional ":type" suffix casts the resolved value to int,
//     float, bool, or string.
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
		if m[2] != "" {
			v, err = applyTypecast(v, m[2])
			if err != nil {
				return nil, fmt.Errorf("context reference <%s:%s>: %w", m[1], m[2], err)
			}
		}
		return v, nil
	}

	// Embedded references: must all resolve to scalars.
	var resolveErr error
	result := contextRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		if resolveErr != nil {
			return match
		}
		// Extract the path (and optional type cast) from inside the angle brackets.
		inner := match[1 : len(match)-1]
		path, cast := splitRefCast(inner)
		v, err := lookupPath(ctx, path)
		if err != nil {
			resolveErr = fmt.Errorf("context reference <%s>: %w", inner, err)
			return match
		}
		if cast != "" {
			v, err = applyTypecast(v, cast)
			if err != nil {
				resolveErr = fmt.Errorf("context reference <%s>: %w", inner, err)
				return match
			}
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

// splitRefCast separates the inner text of a context reference (without angle
// brackets) into its lookup path and an optional type-cast suffix.
// "prep.MIN_SCANLINE:int" → ("prep.MIN_SCANLINE", "int").
// If no ":" is present the cast is empty and the entire string is the path.
func splitRefCast(inner string) (path, cast string) {
	if i := strings.LastIndex(inner, ":"); i >= 0 {
		return inner[:i], inner[i+1:]
	}
	return inner, ""
}

// applyTypecast converts v to the named primitive type.
// Supported casts: int (→ int64), float (→ float64), bool, string.
func applyTypecast(v any, cast string) (any, error) {
	s, ok := scalarString(v)
	if !ok {
		return nil, fmt.Errorf("value of type %T is not a scalar and cannot be cast", v)
	}
	s = strings.TrimSpace(s)
	switch cast {
	case "int":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cast :int: cannot parse %q as integer: %w", s, err)
		}
		return n, nil
	case "float":
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("cast :float: cannot parse %q as float: %w", s, err)
		}
		return f, nil
	case "bool":
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("cast :bool: cannot parse %q as bool: %w", s, err)
		}
		return b, nil
	case "string":
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported type cast :%s (supported: int, float, bool, string)", cast)
	}
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
