package stations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eum/veriproc/internal/store"
	"gopkg.in/yaml.v3"
)

const DefaultSchemaVersion = "veriproc.station/v1"

// Definition is the operator-facing station.yaml shape used by the local
// loader. ContentHash is accepted only as an optional consistency check; the
// loader computes the revision identity from the rest of the definition.
type Definition struct {
	StationID     string            `yaml:"station_id" json:"station_id"`
	ProcType      string            `yaml:"proc_type" json:"proc_type"`
	SchemaVersion string            `yaml:"schema_version,omitempty" json:"schema_version"`
	ContentHash   string            `yaml:"content_hash,omitempty" json:"-"`
	Description   string            `yaml:"description,omitempty" json:"description,omitempty"`
	Scripts       map[string]string `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	Outputs       []string          `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// LoadDir scans root for */station.yaml, computes revision hashes, and seeds
// the supplied registry/store. Files are processed in lexical path order so
// duplicate detection is deterministic.
func LoadDir(ctx context.Context, root string, registry *Registry, st *store.Store) ([]Spec, error) {
	paths, err := stationFiles(root)
	if err != nil {
		return nil, err
	}
	specs := make([]Spec, 0, len(paths))
	seen := map[string]string{}
	for _, path := range paths {
		def, err := ReadDefinition(path)
		if err != nil {
			return nil, err
		}
		spec, err := SpecFromDefinition(def)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		// Resolve script paths to absolute using the station directory so the
		// executor can invoke them without knowing the station config root.
		if len(def.Scripts) > 0 {
			stationDir := filepath.Dir(path)
			spec.Scripts = make(map[string]string, len(def.Scripts))
			for verb, rel := range def.Scripts {
				if filepath.IsAbs(rel) {
					spec.Scripts[verb] = rel
				} else {
					spec.Scripts[verb] = filepath.Join(stationDir, rel)
				}
			}
		}
		if prev, ok := seen[spec.StationID]; ok {
			return nil, fmt.Errorf("%w: station_id=%s in %s and %s", ErrDuplicateStation, spec.StationID, prev, path)
		}
		seen[spec.StationID] = path
		specs = append(specs, spec)
	}
	if err := registry.Seed(ctx, st, specs...); err != nil {
		return nil, err
	}
	return specs, nil
}

func stationFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("stations: read dir %q: %w", root, err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "station.yaml")
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stations: stat %q: %w", path, err)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func ReadDefinition(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("stations: read %q: %w", path, err)
	}
	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return Definition{}, fmt.Errorf("stations: parse %q: %w", path, err)
	}
	return def, nil
}

func SpecFromDefinition(def Definition) (Spec, error) {
	def = normalizeDefinition(def)
	if err := validateDefinition(def); err != nil {
		return Spec{}, err
	}
	computed, err := ComputeContentHash(def)
	if err != nil {
		return Spec{}, err
	}
	if def.ContentHash != "" && def.ContentHash != computed {
		return Spec{}, fmt.Errorf("content_hash mismatch: got %s, computed %s", def.ContentHash, computed)
	}
	return Spec{
		StationID:     def.StationID,
		ProcType:      def.ProcType,
		ContentHash:   computed,
		SchemaVersion: def.SchemaVersion,
		Outputs:       def.Outputs,
	}, nil
}

func SpecFromSeed(stationID, procType string) (Spec, error) {
	return SpecFromDefinition(Definition{StationID: stationID, ProcType: procType})
}

func ComputeContentHash(def Definition) (string, error) {
	def = normalizeDefinition(def)
	def.ContentHash = ""
	payload, err := json.Marshal(def)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeDefinition(def Definition) Definition {
	def.StationID = strings.TrimSpace(def.StationID)
	def.ProcType = strings.TrimSpace(def.ProcType)
	def.SchemaVersion = strings.TrimSpace(def.SchemaVersion)
	if def.SchemaVersion == "" {
		def.SchemaVersion = DefaultSchemaVersion
	}
	def.ContentHash = strings.TrimSpace(def.ContentHash)
	def.Description = strings.TrimSpace(def.Description)
	return def
}

func validateDefinition(def Definition) error {
	if def.StationID == "" {
		return errors.New("station_id must not be empty")
	}
	if def.ProcType == "" {
		return errors.New("proc_type must not be empty")
	}
	if def.SchemaVersion == "" {
		return errors.New("schema_version must not be empty")
	}
	return nil
}
