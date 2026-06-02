package runs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	toml "github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

type jobOrderTemplateInput struct {
	FileType   string
	Category   string
	Path       string
	ObjectKind string
	Present    bool
}

type jobOrderTemplateOutput struct {
	FileType   string
	Name       string
	Directory  string
	ObjectKind string
	Required   bool
}

type jobOrderTemplateOrder struct {
	Start     string
	End       string
	StartUnix string
	EndUnix   string
}

type jobOrderTemplateContext struct {
	RunID           string
	TaskID          string
	RetryIndex      int
	RunRef          string
	StationID       string
	StationName     string
	StationRevision string
	WorkingRoot     string
	ManifestPath    string
	Order           jobOrderTemplateOrder
	Inputs          []jobOrderTemplateInput
	Outputs         []jobOrderTemplateOutput
	Params          map[string]any
	// PrepVars holds the KEY=VALUE pairs emitted by joborder.preprocess_script.
	// Access via {{ index .PrepVars "MIN_SCANLINE" }} or {{ .PrepVars.KEY }}.
	PrepVars        map[string]string
}

func renderTemplateJobOrder(run *store.RunRecord, task *store.TaskRecord, rev *store.StationRevisionRecord, manifest *store.ManifestRecord, outputs []stations.OutputDefinition, manifestPath string, joCfg stations.JobOrderConfig, pathMode string, prepVars map[string]string) ([]byte, error) {
	ctx := buildJobOrderTemplateContext(run, task, rev, manifest, outputs, manifestPath, joCfg.Params, pathMode, prepVars)
	funcs := template.FuncMap{
		"input":    ctx.input,
		"inputs":   ctx.inputs,
		"param":    ctx.param,
		"yamlq":    quoteTemplateValue,
		"tomlq":    quoteTemplateValue,
		"jsonq":    quoteJSONTemplateValue,
		"joinPath": joinTemplatePath,
		"basename": filepath.Base,
		"dirname":  filepath.Dir,
	}
	tpl, err := template.New("joborder").Option("missingkey=error").Funcs(funcs).Parse(joCfg.Template)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, ctx); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	body := buf.Bytes()
	if err := validateRenderedJobOrder(body, joCfg.Format); err != nil {
		return nil, err
	}
	return body, nil
}

func buildJobOrderTemplateContext(run *store.RunRecord, task *store.TaskRecord, rev *store.StationRevisionRecord, manifest *store.ManifestRecord, outputs []stations.OutputDefinition, manifestPath string, params map[string]any, pathMode string, prepVars map[string]string) jobOrderTemplateContext {
	absolutePaths := pathMode == "absolute"
	inputs := make([]jobOrderTemplateInput, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		path := ""
		if entry.Present && entry.Path != "" {
			if absolutePaths {
				path = filepath.ToSlash(filepath.Join(run.WorkingRoot, filepath.FromSlash(entry.Path)))
			} else {
				path = "./" + filepath.ToSlash(entry.Path)
			}
		}
		inputs = append(inputs, jobOrderTemplateInput{
			FileType:   entry.FileType,
			Category:   entry.Category,
			Path:       path,
			ObjectKind: entry.ObjectKind,
			Present:    entry.Present,
		})
	}
	outputDir := filepath.ToSlash(filepath.Join(run.WorkingRoot, "output"))
	if !absolutePaths {
		outputDir = "./output"
	}
	outDocs := make([]jobOrderTemplateOutput, 0, len(outputs))
	for _, out := range outputs {
		outDocs = append(outDocs, jobOrderTemplateOutput{
			FileType:   out.FileType,
			Name:       out.Name,
			Directory:  outputDir,
			ObjectKind: out.ObjectKind,
			Required:   out.Required,
		})
	}
	return jobOrderTemplateContext{
		RunID:           run.RunID,
		TaskID:          run.TaskID,
		RetryIndex:      run.RetryIndex,
		RunRef:          fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex),
		StationID:       rev.StationID,
		StationName:     rev.StationName,
		StationRevision: rev.RevisionID,
		WorkingRoot:     filepath.ToSlash(run.WorkingRoot),
		ManifestPath:    manifestPath,
		Order: jobOrderTemplateOrder{
			Start:     task.WindowStart.UTC().Format(time.RFC3339Nano),
			End:       task.WindowEnd.UTC().Format(time.RFC3339Nano),
			StartUnix: task.WindowStart.UTC().Format("20060102T150405"),
			EndUnix:   task.WindowEnd.UTC().Format("20060102T150405"),
		},
		Inputs:   inputs,
		Outputs:  outDocs,
		Params:   cloneAnyMap(params),
		PrepVars: prepVars,
	}
}

func (ctx jobOrderTemplateContext) input(fileType string, category ...string) (string, error) {
	matches := matchingTemplateInputs(ctx.Inputs, fileType, category...)
	if len(matches) == 0 {
		return "", fmt.Errorf("input %q not found", fileType)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("input %q matched %d entries; use inputs or add a category", fileType, len(matches))
	}
	return matches[0], nil
}

func (ctx jobOrderTemplateContext) inputs(fileType string, category ...string) ([]string, error) {
	matches := matchingTemplateInputs(ctx.Inputs, fileType, category...)
	if len(matches) == 0 {
		return nil, fmt.Errorf("input %q not found", fileType)
	}
	return matches, nil
}

func matchingTemplateInputs(inputs []jobOrderTemplateInput, fileType string, category ...string) []string {
	var categoryFilter string
	if len(category) > 0 {
		categoryFilter = category[0]
	}
	matches := []string{}
	for _, input := range inputs {
		if input.FileType != fileType || !input.Present || input.Path == "" {
			continue
		}
		if categoryFilter != "" && input.Category != categoryFilter {
			continue
		}
		matches = append(matches, input.Path)
	}
	return matches
}

func (ctx jobOrderTemplateContext) param(path string) (any, error) {
	value, err := lookupTemplatePath(ctx.Params, path)
	if err != nil {
		return nil, fmt.Errorf("param %q: %w", path, err)
	}
	return value, nil
}

func lookupTemplatePath(root map[string]any, path string) (any, error) {
	segments := strings.Split(path, ".")
	var current any = root
	for _, segment := range segments {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot traverse into non-mapping segment %q", segment)
		}
		value, ok := m[segment]
		if !ok {
			return nil, fmt.Errorf("key %q not found", segment)
		}
		current = value
	}
	return current, nil
}

func quoteTemplateValue(value any) (string, error) {
	s, ok := templateScalarString(value)
	if !ok {
		return "", fmt.Errorf("cannot quote non-scalar value %T", value)
	}
	return strconv.Quote(s), nil
}

func quoteJSONTemplateValue(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func templateScalarString(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case int:
		return fmt.Sprintf("%d", v), true
	case int64:
		return fmt.Sprintf("%d", v), true
	case float64:
		return fmt.Sprintf("%g", v), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case nil:
		return "", true
	default:
		return "", false
	}
}

func joinTemplatePath(parts ...string) string {
	return filepath.ToSlash(filepath.Join(parts...))
}

func validateRenderedJobOrder(body []byte, format string) error {
	switch format {
	case "yaml", "":
		var doc any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("validate rendered yaml joborder: %w", err)
		}
	case "json":
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("validate rendered json joborder: %w", err)
		}
	case "toml":
		var doc map[string]any
		if err := toml.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("validate rendered toml joborder: %w", err)
		}
	default:
		return fmt.Errorf("unsupported joborder format %q", format)
	}
	return nil
}
