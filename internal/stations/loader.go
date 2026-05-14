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
	StationID      string              `yaml:"station_id" json:"station_id"`
	StationName    string              `yaml:"station_name" json:"station_name"`
	SchemaVersion  string              `yaml:"schema_version,omitempty" json:"schema_version"`
	ContentHash    string              `yaml:"content_hash,omitempty" json:"-"`
	Description    string              `yaml:"description,omitempty" json:"description,omitempty"`
	Execution      Execution           `yaml:"execution,omitempty" json:"execution,omitempty"`
	JobOrder       JobOrderConfig      `yaml:"joborder,omitempty" json:"joborder,omitempty"`
	Inputs         []InputDefinition   `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Outputs        OutputDefinitions   `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	Downstream     []DownstreamTarget  `yaml:"downstream,omitempty" json:"downstream,omitempty"`
	Publication    PublicationPolicy   `yaml:"publication,omitempty" json:"publication,omitempty"`
	RollingFolders map[string][]string `yaml:"rolling_folders,omitempty" json:"rolling_folders,omitempty"`
	Metadata       map[string]string   `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// Execution describes how to invoke the station workload.
type Execution struct {
	Mode       string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Executable string   `yaml:"executable,omitempty" json:"executable,omitempty"`
	Args       []string `yaml:"args,omitempty" json:"args,omitempty"`
}

// JobOrderConfig holds the station's joborder rendering configuration.
type JobOrderConfig struct {
	// Format is one of "yaml", "toml", "json", or "none". Defaults to "yaml".
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	// Name is the filename for the joborder file, relative to the working root.
	// Defaults to "joborder.yaml", "joborder.toml", or "joborder.json" per format.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Include is an arbitrary mapping merged into the generated joborder document.
	// Context references within Include are resolved before rendering.
	Include map[string]any `yaml:"include,omitempty" json:"include,omitempty"`
}

type InputDefinition struct {
	FileType        string `yaml:"file_type" json:"file_type"`
	Category        string `yaml:"category" json:"category"`
	ObjectKind      string `yaml:"object_kind,omitempty" json:"object_kind,omitempty"`
	Pattern         string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	FilenamePattern string `yaml:"filename_pattern,omitempty" json:"filename_pattern,omitempty"`
	WindowMatch     string `yaml:"window_match,omitempty" json:"window_match,omitempty"`
	Margins         []int  `yaml:"margins,omitempty" json:"margins,omitempty"`
	Mandatory       bool   `yaml:"mandatory,omitempty" json:"mandatory,omitempty"`
	Optional        bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

type OutputDefinition struct {
	Name            string `yaml:"name,omitempty" json:"name,omitempty"`
	FileType        string `yaml:"file_type,omitempty" json:"file_type,omitempty"`
	ObjectKind      string `yaml:"object_kind,omitempty" json:"object_kind,omitempty"`
	Pattern         string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	FilenamePattern string `yaml:"filename_pattern,omitempty" json:"filename_pattern,omitempty"`
	Required        bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Mandatory       bool   `yaml:"mandatory,omitempty" json:"mandatory,omitempty"`
	Publish         any    `yaml:"publish,omitempty" json:"publish,omitempty"`
}

type OutputDefinitions []OutputDefinition

func (o *OutputDefinitions) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("outputs must be a sequence")
	}
	out := make([]OutputDefinition, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("outputs entries must be mappings")
		}
		var def OutputDefinition
		if err := item.Decode(&def); err != nil {
			return err
		}
		if def.FileType == "" {
			def.FileType = def.Name
		}
		out = append(out, def)
	}
	*o = out
	return nil
}

type DownstreamTarget struct {
	StationID string `yaml:"station_id" json:"station_id"`
}

type PublicationPolicy struct {
	Enabled       bool     `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	ArchiveID     string   `yaml:"archive_id,omitempty" json:"archive_id,omitempty"`
	Mode          string   `yaml:"mode,omitempty" json:"mode,omitempty"`
	TargetSubpath string   `yaml:"target_subpath,omitempty" json:"target_subpath,omitempty"`
	Outputs       []string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
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
		// Resolve executable path to absolute using the station directory so the
		// executor can invoke it without knowing the station config root.
		// The spec's ContentHash is computed from the relative path (deterministic);
		// we update spec.Execution.Executable separately so the executor and DB
		// get the runtime-resolved absolute path.
		if def.Execution.Executable != "" && !filepath.IsAbs(def.Execution.Executable) {
			stationDir := filepath.Dir(path)
			absStationDir, err := filepath.Abs(stationDir)
			if err != nil {
				return nil, fmt.Errorf("stations: resolve %q: %w", stationDir, err)
			}
			spec.Execution.Executable = filepath.Join(absStationDir, def.Execution.Executable)
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
		StationID:      def.StationID,
		StationName:    def.StationName,
		ContentHash:    computed,
		SchemaVersion:  def.SchemaVersion,
		Execution:      def.Execution,
		JobOrder:       def.JobOrder,
		Inputs:         def.Inputs,
		Outputs:        []OutputDefinition(def.Outputs),
		Downstream:     def.Downstream,
		Publication:    def.Publication,
		RollingFolders: def.RollingFolders,
	}, nil
}

func SpecFromSeed(stationID, stationName string) (Spec, error) {
	return SpecFromDefinition(Definition{StationID: stationID, StationName: stationName})
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
	def.StationName = strings.TrimSpace(def.StationName)
	def.SchemaVersion = strings.TrimSpace(def.SchemaVersion)
	if def.SchemaVersion == "" {
		def.SchemaVersion = DefaultSchemaVersion
	}
	def.ContentHash = strings.TrimSpace(def.ContentHash)
	def.Description = strings.TrimSpace(def.Description)
	def.Execution.Mode = strings.TrimSpace(def.Execution.Mode)
	def.Execution.Executable = strings.TrimSpace(def.Execution.Executable)
	// Normalize joborder format default.
	def.JobOrder.Format = strings.TrimSpace(strings.ToLower(def.JobOrder.Format))
	if def.JobOrder.Format == "" {
		def.JobOrder.Format = "yaml"
	}
	def.JobOrder.Name = strings.TrimSpace(def.JobOrder.Name)
	if def.JobOrder.Name == "" {
		switch def.JobOrder.Format {
		case "toml":
			def.JobOrder.Name = "joborder.toml"
		case "json":
			def.JobOrder.Name = "joborder.json"
		case "none":
			def.JobOrder.Name = ""
		default:
			def.JobOrder.Name = "joborder.yaml"
		}
	}
	for i := range def.Inputs {
		def.Inputs[i].FileType = strings.TrimSpace(def.Inputs[i].FileType)
		def.Inputs[i].Category = strings.TrimSpace(def.Inputs[i].Category)
		def.Inputs[i].ObjectKind = strings.TrimSpace(strings.ToLower(def.Inputs[i].ObjectKind))
		def.Inputs[i].Pattern = strings.TrimSpace(def.Inputs[i].Pattern)
		if def.Inputs[i].Mandatory {
			def.Inputs[i].Optional = false
		}
	}
	for i := range def.Outputs {
		def.Outputs[i].Name = strings.TrimSpace(def.Outputs[i].Name)
		def.Outputs[i].FileType = strings.TrimSpace(def.Outputs[i].FileType)
		def.Outputs[i].ObjectKind = strings.TrimSpace(strings.ToLower(def.Outputs[i].ObjectKind))
		def.Outputs[i].Pattern = strings.TrimSpace(def.Outputs[i].Pattern)
		// Name is intentionally NOT defaulted to FileType: when neither Name
		// nor Pattern is set, resolveOutputPath falls through to filename-pattern
		// scanning, which uses the instance-level filename_pattern to find any
		// matching file in the output directory. Set Name explicitly only when
		// a literal output filename is required.
		if def.Outputs[i].FileType == "" {
			def.Outputs[i].FileType = def.Outputs[i].Name
		}
		if def.Outputs[i].Mandatory {
			def.Outputs[i].Required = true
		}
		if !def.Outputs[i].Required {
			def.Outputs[i].Required = true
		}
	}
	for i := range def.Downstream {
		def.Downstream[i].StationID = strings.TrimSpace(def.Downstream[i].StationID)
	}
	def.Publication.ArchiveID = strings.TrimSpace(def.Publication.ArchiveID)
	def.Publication.Mode = strings.TrimSpace(def.Publication.Mode)
	if def.Publication.Mode == "" {
		def.Publication.Mode = "copy"
	}
	return def
}

func validateDefinition(def Definition) error {
	if def.StationID == "" {
		return errors.New("station_id must not be empty")
	}
	if def.StationName == "" {
		return errors.New("station_name must not be empty")
	}
	if def.SchemaVersion == "" {
		return errors.New("schema_version must not be empty")
	}
	// Validate joborder section.
	switch def.JobOrder.Format {
	case "yaml", "toml", "json", "none":
		// valid
	default:
		return fmt.Errorf("joborder.format %q invalid; must be yaml, toml, json, or none", def.JobOrder.Format)
	}
	if def.JobOrder.Format == "none" {
		if def.JobOrder.Name != "" {
			return errors.New("joborder.name must be absent when format is none")
		}
		if len(def.JobOrder.Include) > 0 {
			return errors.New("joborder.include must be absent when format is none")
		}
	}
	if def.JobOrder.Name != "" {
		if filepath.IsAbs(def.JobOrder.Name) {
			return fmt.Errorf("joborder.name %q must be relative", def.JobOrder.Name)
		}
		clean := filepath.Clean(def.JobOrder.Name)
		if strings.HasPrefix(clean, "..") {
			return fmt.Errorf("joborder.name %q must not escape the working root", def.JobOrder.Name)
		}
	}
	for _, input := range def.Inputs {
		if input.FileType == "" {
			return errors.New("input file_type must not be empty")
		}
		if input.Category == "" {
			return fmt.Errorf("input %s category must not be empty", input.FileType)
		}
		if !validObjectKind(input.ObjectKind) {
			return fmt.Errorf("input %s object_kind must be regular_file or directory", input.FileType)
		}
	}
	for _, out := range def.Outputs {
		if out.FileType == "" {
			return errors.New("output file_type must not be empty")
		}
		if !validObjectKind(out.ObjectKind) {
			return fmt.Errorf("output %s object_kind must be regular_file or directory", out.FileType)
		}
		if out.Name == "" && out.Pattern == "" && out.FileType == "" {
			return errors.New("output must define file_type, name, or pattern")
		}
	}
	for _, down := range def.Downstream {
		if down.StationID == "" {
			return errors.New("downstream target must define station_id")
		}
	}
	if def.Publication.Enabled && def.Publication.ArchiveID == "" {
		return errors.New("publication archive_id must not be empty when enabled")
	}
	return nil
}

func validObjectKind(kind string) bool {
	switch kind {
	case "", "regular_file", "directory":
		return true
	default:
		return false
	}
}
