// Package canonjson produces a deterministic JSON encoding suitable for
// hashing. Object keys are sorted recursively. Numbers are rendered by
// encoding/json without normalization.
package canonjson

import (
	"encoding/json"
	"sort"
	"strings"
)

// Marshal returns the canonical JSON encoding of v.
func Marshal(v any) (json.RawMessage, error) {
	var b strings.Builder
	if err := write(&b, v); err != nil {
		return nil, err
	}
	return json.RawMessage(b.String()), nil
}

func write(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kj, _ := json.Marshal(k)
			b.Write(kj)
			b.WriteByte(':')
			if err := write(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := write(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(raw)
	}
	return nil
}
