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
	"strconv"
	"strings"
	"time"

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
	RollingFolders map[string][]string `yaml:"rolling_folders,omitempty" json:"rolling_folders,omitempty"`
}

// Execution describes how to invoke the station workload.
type Execution struct {
	Mode       string          `yaml:"mode,omitempty" json:"mode,omitempty"`
	Executable string          `yaml:"executable,omitempty" json:"executable,omitempty"`
	Args       []string        `yaml:"args,omitempty" json:"args,omitempty"`
	Resources  ResourceRequest `yaml:"resources,omitempty" json:"resources,omitempty"`
	Slurm      SlurmSettings   `yaml:"slurm,omitempty" json:"slurm,omitempty"`
	Container  ContainerConfig `yaml:"container,omitempty" json:"container,omitempty"`
}

// ResourceRequest describes station-level scheduler resource requirements.
type ResourceRequest struct {
	CPUsPerTask int    `yaml:"cpus_per_task,omitempty" json:"cpus_per_task,omitempty"`
	MemGB       int    `yaml:"mem_gb,omitempty" json:"mem_gb,omitempty"`
	Walltime    string `yaml:"walltime,omitempty" json:"walltime,omitempty"`
}

// SlurmSettings describes station-level SLURM submission overrides.
type SlurmSettings struct {
	Partition string   `yaml:"partition,omitempty" json:"partition,omitempty"`
	Account   string   `yaml:"account,omitempty" json:"account,omitempty"`
	QOS       string   `yaml:"qos,omitempty" json:"qos,omitempty"`
	ExtraArgs []string `yaml:"extra_args,omitempty" json:"extra_args,omitempty"`
}

// ContainerConfig describes station-level container runtime settings.
type ContainerConfig struct {
	Image  string   `yaml:"image,omitempty" json:"image,omitempty"`
	Mounts []string `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	User   string   `yaml:"user,omitempty" json:"user,omitempty"`
}

// IsZero reports whether the station declares no execution configuration.
func (e Execution) IsZero() bool {
	return e.Mode == "" && e.Executable == "" && len(e.Args) == 0 &&
		e.Resources.CPUsPerTask == 0 && e.Resources.MemGB == 0 && e.Resources.Walltime == "" &&
		e.Slurm.Partition == "" && e.Slurm.Account == "" && e.Slurm.QOS == "" && len(e.Slurm.ExtraArgs) == 0 &&
		e.Container.Image == "" && len(e.Container.Mounts) == 0 && e.Container.User == ""
}

// JobOrderConfig holds the station's joborder rendering configuration.
type JobOrderConfig struct {
	// Renderer selects the joborder rendering backend. Empty/default uses the
	// built-in VeriProc document; "template" renders Template as a Go template.
	Renderer string `yaml:"renderer,omitempty" json:"renderer,omitempty"`
	// Format is one of "yaml", "toml", "json", or "none". Defaults to "yaml".
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	// Name is the filename for the joborder file, relative to the working root.
	// Defaults to "joborder.yaml", "joborder.toml", or "joborder.json" per format.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// Paths overrides the instance-level generators.job_order.paths setting for
	// this station only. Accepted values: "relative" (default), "absolute".
	// When omitted the instance-level setting is used.
	Paths string `yaml:"paths,omitempty" json:"paths,omitempty"`
	// TemplateFile is a path relative to the station directory. When renderer is
	// "template", ReadDefinition loads it into Template so the station revision
	// hash changes when the template content changes.
	TemplateFile string `yaml:"template_file,omitempty" json:"template_file,omitempty"`
	// Template is an inline template body, or the loaded contents of TemplateFile.
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
	// Params is an arbitrary mapping made available to template joborders.
	Params map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
	// Include is an arbitrary mapping merged into the generated joborder document.
	// Context references within Include are resolved before rendering.
	Include map[string]any `yaml:"include,omitempty" json:"include,omitempty"`
	// Meta controls whether the veriproc_meta block is emitted by the default
	// renderer. Set to false to omit it for processors that reject unknown keys.
	// Has no effect when renderer is "template" (template controls everything).
	Meta *bool `yaml:"meta,omitempty" json:"meta,omitempty"`
}

// InputFilter declares a single filtering rule applied to input candidates
// during manifest resolution. Multiple rules in an input's filter list are
// applied in order and all must pass (AND semantics).
//
// Supported rules:
//
//	"filename_component" – Keeps only candidates whose parsed filename
//	component named by Component equals the value extracted from the first
//	winner already resolved for SourceFileType. The component name is
//	case-insensitive (matched as lowercase). When the source file type has not
//	been resolved yet, or produced no winners, the filter is skipped.
type InputFilter struct {
	Rule           string `yaml:"rule" json:"rule"`
	Component      string `yaml:"component,omitempty" json:"component,omitempty"`
	SourceFileType string `yaml:"source_file_type,omitempty" json:"source_file_type,omitempty"`
}

type InputDefinition struct {
	FileType        string        `yaml:"file_type" json:"file_type"`
	Category        string        `yaml:"category" json:"category"`
	ObjectKind      string        `yaml:"object_kind,omitempty" json:"object_kind,omitempty"`
	Pattern         string        `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	FilenamePattern string        `yaml:"filename_pattern,omitempty" json:"filename_pattern,omitempty"`
	WindowMatch     string        `yaml:"window_match,omitempty" json:"window_match,omitempty"`
	Margins         []int         `yaml:"margins,omitempty" json:"margins,omitempty"`
	// Mandatory is a pointer so the loader can distinguish an explicit
	// "mandatory: false" (non-blocking input) from an omitted field. It is
	// normalized into Optional at load time; the matcher reads Optional only.
	Mandatory *bool         `yaml:"mandatory,omitempty" json:"mandatory,omitempty"`
	Optional  bool          `yaml:"optional,omitempty" json:"optional,omitempty"`
	Filters   []InputFilter `yaml:"filter,omitempty" json:"filter,omitempty"`
}

type OutputDefinition struct {
	Name            string `yaml:"name,omitempty" json:"name,omitempty"`
	FileType        string `yaml:"file_type,omitempty" json:"file_type,omitempty"`
	ObjectKind      string `yaml:"object_kind,omitempty" json:"object_kind,omitempty"`
	Pattern         string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	FilenamePattern string `yaml:"filename_pattern,omitempty" json:"filename_pattern,omitempty"`
	Required        bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Mandatory       bool   `yaml:"mandatory,omitempty" json:"mandatory,omitempty"`
	// Multiple, when true, causes all files matching the filename pattern (or
	// Pattern glob) to be captured as individual output artifacts instead of
	// only the newest single match. Use for fan-out stations that write a
	// variable number of output files per run.
	Multiple bool           `yaml:"multiple,omitempty" json:"multiple,omitempty"`
	Publish  *OutputPublish `yaml:"publish,omitempty" json:"publish,omitempty"`
}

// OutputPublish declares that a validated output should be published to a
// rolling archive after run finalization. The rolling_archive field must
// resolve to a configured archive in the instance configuration. Mode
// defaults to "copy" when absent.
type OutputPublish struct {
	RollingArchive string `yaml:"rolling_archive,omitempty" json:"rolling_archive,omitempty"`
	Mode           string `yaml:"mode,omitempty" json:"mode,omitempty"`
	TargetSubpath  string `yaml:"target_subpath,omitempty" json:"target_subpath,omitempty"`
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
	JoinID    string `yaml:"join_id,omitempty" json:"join_id,omitempty"`
	// Mode controls when the downstream is triggered.
	// "normal" (default, empty): triggered immediately on run finalization.
	// "task_out": declared as an allowed target, but concrete child tasks are
	//             created only from algorithm-produced task-out.yaml entries.
	// "fan_in": triggered only when the split group spawned by this station
	//           reaches "complete" state (all members canonical).
	// "join": creates or wakes one shared downstream task, identified by
	//         join_id, target station, and processing window. The task waits
	//         until all mandatory target inputs are resolvable.
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
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
	if err := loadJobOrderTemplate(path, &def); err != nil {
		return Definition{}, err
	}
	return def, nil
}

func loadJobOrderTemplate(stationPath string, def *Definition) error {
	templateFile := strings.TrimSpace(def.JobOrder.TemplateFile)
	if templateFile == "" {
		return nil
	}
	if filepath.IsAbs(templateFile) {
		return fmt.Errorf("%s: joborder.template_file %q must be relative", stationPath, templateFile)
	}
	clean := filepath.Clean(templateFile)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: joborder.template_file %q must not escape the station directory", stationPath, templateFile)
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(stationPath), clean))
	if err != nil {
		return fmt.Errorf("%s: read joborder.template_file %q: %w", stationPath, templateFile, err)
	}
	def.JobOrder.TemplateFile = templateFile
	def.JobOrder.Template = string(body)
	return nil
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
	def.Execution.Mode = strings.TrimSpace(strings.ToLower(def.Execution.Mode))
	def.Execution.Executable = strings.TrimSpace(def.Execution.Executable)
	def.Execution.Resources.Walltime = strings.TrimSpace(def.Execution.Resources.Walltime)
	def.Execution.Slurm.Partition = strings.TrimSpace(def.Execution.Slurm.Partition)
	def.Execution.Slurm.Account = strings.TrimSpace(def.Execution.Slurm.Account)
	def.Execution.Slurm.QOS = strings.TrimSpace(def.Execution.Slurm.QOS)
	def.Execution.Container.Image = strings.TrimSpace(def.Execution.Container.Image)
	def.Execution.Container.User = strings.TrimSpace(def.Execution.Container.User)
	for i := range def.Execution.Slurm.ExtraArgs {
		def.Execution.Slurm.ExtraArgs[i] = strings.TrimSpace(def.Execution.Slurm.ExtraArgs[i])
	}
	for i := range def.Execution.Container.Mounts {
		def.Execution.Container.Mounts[i] = strings.TrimSpace(def.Execution.Container.Mounts[i])
	}
	// Normalize joborder format default.
	def.JobOrder.Renderer = strings.TrimSpace(strings.ToLower(def.JobOrder.Renderer))
	if def.JobOrder.Renderer == "" {
		def.JobOrder.Renderer = "default"
	}
	def.JobOrder.Format = strings.TrimSpace(strings.ToLower(def.JobOrder.Format))
	if def.JobOrder.Format == "" {
		def.JobOrder.Format = "yaml"
	}
	def.JobOrder.Name = strings.TrimSpace(def.JobOrder.Name)
	def.JobOrder.TemplateFile = strings.TrimSpace(def.JobOrder.TemplateFile)
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
	def.JobOrder.Paths = strings.TrimSpace(strings.ToLower(def.JobOrder.Paths))
	for i := range def.Inputs {
		def.Inputs[i].FileType = strings.TrimSpace(def.Inputs[i].FileType)
		def.Inputs[i].Category = strings.TrimSpace(def.Inputs[i].Category)
		def.Inputs[i].ObjectKind = strings.TrimSpace(strings.ToLower(def.Inputs[i].ObjectKind))
		def.Inputs[i].Pattern = strings.TrimSpace(def.Inputs[i].Pattern)
		// An explicit "mandatory" setting is authoritative and drives the
		// canonical Optional flag the matcher reads: mandatory:true => not
		// optional; mandatory:false => optional. When omitted, Optional keeps
		// its declared value (default false, i.e. mandatory).
		if def.Inputs[i].Mandatory != nil {
			def.Inputs[i].Optional = !*def.Inputs[i].Mandatory
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
		def.Downstream[i].JoinID = strings.TrimSpace(def.Downstream[i].JoinID)
		def.Downstream[i].Mode = strings.TrimSpace(strings.ToLower(def.Downstream[i].Mode))
	}
	for i := range def.Outputs {
		if def.Outputs[i].Publish != nil {
			def.Outputs[i].Publish.RollingArchive = strings.TrimSpace(def.Outputs[i].Publish.RollingArchive)
			def.Outputs[i].Publish.Mode = strings.TrimSpace(def.Outputs[i].Publish.Mode)
			if def.Outputs[i].Publish.Mode == "" {
				def.Outputs[i].Publish.Mode = "copy"
			}
		}
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
	switch def.Execution.Mode {
	case "", "local", "slurm-native", "slurm-docker":
		// valid
	default:
		return fmt.Errorf("execution.mode %q invalid", def.Execution.Mode)
	}
	if def.Execution.Resources.CPUsPerTask < 0 || def.Execution.Resources.MemGB < 0 {
		return errors.New("execution.resources values must be non-negative")
	}
	if def.Execution.Resources.Walltime != "" {
		if _, err := validateWalltime(def.Execution.Resources.Walltime); err != nil {
			return fmt.Errorf("execution.resources.walltime %q invalid: %w", def.Execution.Resources.Walltime, err)
		}
	}
	for _, arg := range def.Execution.Slurm.ExtraArgs {
		if arg == "" {
			return errors.New("execution.slurm.extra_args must not contain empty entries")
		}
	}
	if def.Execution.Mode == "slurm-docker" && def.Execution.Container.Image == "" {
		return errors.New("execution.container.image is required for slurm-docker")
	}
	for _, mount := range def.Execution.Container.Mounts {
		if mount == "" {
			return errors.New("execution.container.mounts must not contain empty entries")
		}
	}
	// Validate joborder section.
	switch def.JobOrder.Renderer {
	case "default", "template":
		// valid
	default:
		return fmt.Errorf("joborder.renderer %q invalid; must be default or template", def.JobOrder.Renderer)
	}
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
		if def.JobOrder.TemplateFile != "" || def.JobOrder.Template != "" || len(def.JobOrder.Params) > 0 {
			return errors.New("joborder template fields must be absent when format is none")
		}
	}
	if def.JobOrder.Name != "" {
		if filepath.IsAbs(def.JobOrder.Name) {
			return fmt.Errorf("joborder.name %q must be relative", def.JobOrder.Name)
		}
		clean := filepath.Clean(def.JobOrder.Name)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("joborder.name %q must not escape the working root", def.JobOrder.Name)
		}
	}
	if def.JobOrder.TemplateFile != "" {
		if filepath.IsAbs(def.JobOrder.TemplateFile) {
			return fmt.Errorf("joborder.template_file %q must be relative", def.JobOrder.TemplateFile)
		}
		clean := filepath.Clean(def.JobOrder.TemplateFile)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("joborder.template_file %q must not escape the station directory", def.JobOrder.TemplateFile)
		}
	}
	if def.JobOrder.Renderer == "template" && def.JobOrder.Format != "none" && strings.TrimSpace(def.JobOrder.Template) == "" {
		return errors.New("joborder.template or joborder.template_file is required when renderer is template")
	}
	if def.JobOrder.Renderer == "template" && len(def.JobOrder.Include) > 0 {
		return errors.New("joborder.include is only supported by the default renderer; use joborder.params with renderer template")
	}
	if def.JobOrder.Renderer == "default" && (def.JobOrder.TemplateFile != "" || def.JobOrder.Template != "" || len(def.JobOrder.Params) > 0) {
		return errors.New("joborder template fields require renderer: template")
	}
	switch def.JobOrder.Paths {
	case "", "relative", "absolute":
		// valid
	default:
		return fmt.Errorf("joborder.paths %q invalid; must be \"relative\" or \"absolute\"", def.JobOrder.Paths)
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
		for fi, f := range input.Filters {
			if err := validateInputFilter(input.FileType, fi, f); err != nil {
				return err
			}
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
		switch down.Mode {
		case "", "normal", "task_out", "fan_in", "join":
			// valid
		default:
			return fmt.Errorf("downstream target %s mode %q invalid", down.StationID, down.Mode)
		}
		if down.Mode == "join" && down.JoinID == "" {
			return fmt.Errorf("downstream target %s mode join requires join_id", down.StationID)
		}
	}
	for _, out := range def.Outputs {
		if out.Publish != nil && out.Publish.RollingArchive == "" {
			return fmt.Errorf("output %s publish.rolling_archive must not be empty", out.FileType)
		}
	}
	return nil
}

func validateWalltime(value string) (string, error) {
	if strings.Contains(value, ":") {
		return value, nil
	}
	d, err := parseDurationLike(value)
	if err != nil {
		return "", err
	}
	totalSeconds := int64(d / time.Second)
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds), nil
}

func parseDurationLike(value string) (time.Duration, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return d, nil
	}
	if len(value) < 3 || value[0] != 'P' || value[1] != 'T' {
		return 0, fmt.Errorf("expected Go duration or ISO-8601 PT duration")
	}
	var total time.Duration
	start := 2
	for i := 2; i < len(value); i++ {
		switch value[i] {
		case 'H', 'M', 'S':
			if start == i {
				return 0, fmt.Errorf("missing number before %c", value[i])
			}
			n, err := strconv.Atoi(value[start:i])
			if err != nil {
				return 0, err
			}
			switch value[i] {
			case 'H':
				total += time.Duration(n) * time.Hour
			case 'M':
				total += time.Duration(n) * time.Minute
			case 'S':
				total += time.Duration(n) * time.Second
			}
			start = i + 1
		}
	}
	if start != len(value) || total == 0 {
		return 0, fmt.Errorf("expected Go duration or ISO-8601 PT duration")
	}
	return total, nil
}

func validObjectKind(kind string) bool {
	switch kind {
	case "", "regular_file", "directory":
		return true
	default:
		return false
	}
}

// validateInputFilter validates a single InputFilter entry.
func validateInputFilter(fileType string, idx int, f InputFilter) error {
	switch f.Rule {
	case "filename_component":
		if f.Component == "" {
			return fmt.Errorf("input %s filter[%d]: rule %q requires component", fileType, idx, f.Rule)
		}
		if f.SourceFileType == "" {
			return fmt.Errorf("input %s filter[%d]: rule %q requires source_file_type", fileType, idx, f.Rule)
		}
		if f.SourceFileType == fileType {
			return fmt.Errorf("input %s filter[%d]: source_file_type must not refer to its own file_type", fileType, idx)
		}
	case "":
		return fmt.Errorf("input %s filter[%d]: rule must not be empty", fileType, idx)
	default:
		return fmt.Errorf("input %s filter[%d]: unknown rule %q", fileType, idx, f.Rule)
	}
	return nil
}

