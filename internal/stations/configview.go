package stations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrNoConfigSource is returned when the config view is requested but the
// service was never wired to a station resolver (daemon startup miswiring).
var ErrNoConfigSource = errors.New("stations: config source not configured")

// DescriptionProvider is an optional extension to Resolver that returns the
// startup-loaded station description. It is implemented by Registry.
type DescriptionProvider interface {
	Description(stationID string) (string, bool)
}

// secretEnvPattern matches environment variable names that hold secret
// material. Values of matching variables are redacted from the config view.
var secretEnvPattern = regexp.MustCompile(`(?i)(token|password|secret|key|credential)`)

// redactionMarker replaces any secret value found in the config view.
const redactionMarker = "[REDACTED]"

// ConfigView is the response document of GET /api/v1/stations/{id}/config.
// Config holds the resolved configuration document (marshalled from the
// startup-loaded revision blobs, context references resolved against instance
// definitions only, runtime references left literal, secret values redacted).
// ConfigYAML is the same document re-serialized as YAML for direct display.
type ConfigView struct {
	StationID     string    `json:"station_id"`
	StationName   string    `json:"station_name"`
	ContentHash   string    `json:"content_hash"`
	SchemaVersion string    `json:"schema_version"`
	EffectiveAt   time.Time `json:"effective_at"`
	Config        any       `json:"config"`
	ConfigYAML    string    `json:"config_yaml"`
}

// SetConfigSource wires the service to the startup-loaded station registry,
// the instance definitions (context reference resolution scope), and the
// startup environment snapshot (secret-value redaction). It is called once
// during daemon wiring.
func (s *Service) SetConfigSource(resolver Resolver, descriptions DescriptionProvider, definitions map[string]any, env map[string]string) {
	s.configResolver = resolver
	s.configDescriptions = descriptions
	s.configDefinitions = definitions
	s.configEnv = env
}

// ConfigView assembles the resolved configuration view for the station as
// loaded at daemon startup (see the Station Config Endpoint requirement).
func (s *Service) ConfigView(ctx context.Context, stationID string) (*ConfigView, error) {
	if s.configResolver == nil {
		return nil, ErrNoConfigSource
	}
	rec, err := s.configResolver.Resolve(ctx, stationID)
	if err != nil {
		return nil, err
	}

	doc := map[string]any{}
	if desc, ok := s.configDescriptions.Description(stationID); ok && desc != "" {
		doc["description"] = desc
	}
	if err := unmarshalInto(doc, "execution", rec.DeclaredExecution); err != nil {
		return nil, err
	}
	if err := unmarshalInto(doc, "joborder", rec.DeclaredJobOrder); err != nil {
		return nil, err
	}
	if err := unmarshalInto(doc, "inputs", rec.DeclaredInputs); err != nil {
		return nil, err
	}
	if err := unmarshalInto(doc, "outputs", rec.DeclaredOutputs); err != nil {
		return nil, err
	}
	if err := unmarshalInto(doc, "downstream", rec.DeclaredDownstream); err != nil {
		return nil, err
	}
	if err := unmarshalInto(doc, "rolling_folders", rec.RollingFolders); err != nil {
		return nil, err
	}

	resolved := resolveConfigDoc(doc, s.configDefinitions)
	redactSecrets(resolved, s.configEnv)

	out := &ConfigView{
		StationID:      rec.StationID,
		StationName:    rec.StationName,
		ContentHash:    rec.ContentHash,
		SchemaVersion:  rec.SchemaVersion,
		EffectiveAt:    rec.EffectiveAt,
		Config:         resolved,
	}
	yamlBytes, err := yaml.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("stations: marshal config yaml for %s: %w", stationID, err)
	}
	out.ConfigYAML = string(yamlBytes)
	return out, nil
}

// unmarshalInto decodes a JSON blob from the station revision record into
// doc[key]. Empty blobs are skipped.
func unmarshalInto(doc map[string]any, key, blob string) error {
	if blob == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(blob), &v); err != nil {
		return fmt.Errorf("stations: decode declared %s: %w", key, err)
	}
	doc[key] = v
	return nil
}

// resolveConfigDoc walks the document resolving context references in every
// string leaf against the definitions-only context. References that cannot be
// resolved there — the per-run runtime keys such as <start>, <working_root>,
// or <prep.*> — are left as literal text rather than erroring.
func resolveConfigDoc(doc map[string]any, definitions map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = resolveConfigValue(v, definitions)
	}
	return out
}

func resolveConfigValue(v any, definitions map[string]any) any {
	switch t := v.(type) {
	case string:
		return resolveConfigString(t, definitions)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = resolveConfigValue(vv, definitions)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = resolveConfigValue(vv, definitions)
		}
		return out
	default:
		return v
	}
}

// resolveConfigString resolves context references in s against the
// definitions-only context, leniently: a full reference that resolves keeps
// its scalar string form; references that do not resolve (runtime keys) or
// resolve to non-scalars are kept verbatim.
func resolveConfigString(s string, definitions map[string]any) string {
	if !strings.ContainsRune(s, '<') || definitions == nil {
		return s
	}
	if m := fullRefPattern.FindStringSubmatch(s); m != nil {
		v, err := lookupPath(definitions, m[1])
		if err != nil {
			return s // runtime reference: keep literal
		}
		if m[2] != "" {
			if cast, err := applyTypecast(v, m[2]); err == nil {
				v = cast
			}
		}
		if sv, ok := scalarString(v); ok {
			return sv
		}
		return s // non-scalar: keep literal
	}
	return contextRefPattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[1 : len(match)-1]
		path, cast := splitRefCast(inner)
		v, err := lookupPath(definitions, path)
		if err != nil {
			return match // runtime reference: keep literal
		}
		if cast != "" {
			if casted, err := applyTypecast(v, cast); err == nil {
				v = casted
			} else {
				return match
			}
		}
		if sv, ok := scalarString(v); ok {
			return sv
		}
		return match // non-scalar embedded: keep literal
	})
}

// redactSecrets replaces every occurrence of a secret environment variable's
// value in every string leaf of the document with a redaction marker. Only
// variables whose names match the secret pattern are considered.
func redactSecrets(doc map[string]any, env map[string]string) {
	if len(env) == 0 {
		return
	}
	secrets := make([]string, 0, len(env))
	for name, val := range env {
		if val != "" && secretEnvPattern.MatchString(name) {
			secrets = append(secrets, val)
		}
	}
	if len(secrets) == 0 {
		return
	}
	redactValue(doc, secrets)
}

func redactValue(v any, secrets []string) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			if s, ok := vv.(string); ok {
				t[k] = redactString(s, secrets)
			} else {
				redactValue(vv, secrets)
			}
		}
	case []any:
		for i, vv := range t {
			if s, ok := vv.(string); ok {
				t[i] = redactString(s, secrets)
			} else {
				redactValue(vv, secrets)
			}
		}
	}
}

func redactString(s string, secrets []string) string {
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, redactionMarker)
		}
	}
	return s
}
