package policy

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	ChecksumAvailableOnly = "available_only"
	ChecksumNone          = "none"
	ChecksumRequired      = "required"
)

type Naming struct {
	TimestampFormat TimestampFormat `yaml:"timestamp_format"`
	WorkingRoot     WorkingRoot     `yaml:"working_root"`
	Filenames       Filenames       `yaml:"filenames"`
}

type TimestampFormat struct {
	TaskWindow   string `yaml:"task_window"`
	RuntimeEvent string `yaml:"runtime_event"`
}

type WorkingRoot struct {
	StationSegment  string `yaml:"station_segment"`
	TaskSegment     string `yaml:"task_segment"`
	RunSegment      string `yaml:"run_segment"`
	CollisionSuffix string `yaml:"collision_suffix"`
}

type Filenames struct {
	AllowedCharset   string                   `yaml:"allowed_charset" json:"allowed_charset"`
	ReplaceInvalid   string                   `yaml:"replace_invalid" json:"replace_invalid"`
	MaxSegmentLength int                      `yaml:"max_segment_length" json:"max_segment_length"`
	StationCase      string                   `yaml:"station_case" json:"station_case"`
	FilenamePattern  string                   `yaml:"filename_pattern" json:"filename_pattern"`
	Components       map[string]ComponentRule `yaml:"components" json:"components"`
}

type ComponentRule struct {
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Length  int    `yaml:"length,omitempty" json:"length,omitempty"`
	Charset string `yaml:"charset,omitempty" json:"charset,omitempty"`
	Format  string `yaml:"format,omitempty" json:"format,omitempty"`
}

type Integrity struct {
	ChecksumPolicy           string   `yaml:"checksum_policy"`
	AllowedAlgorithms        []string `yaml:"allowed_algorithms"`
	RecordSizeWhenAvailable  bool     `yaml:"record_size_when_available"`
	RecordMTimeWhenAvailable bool     `yaml:"record_mtime_when_available"`
}

func DefaultNaming() Naming {
	return Naming{
		TimestampFormat: TimestampFormat{TaskWindow: "compact-utc-millis", RuntimeEvent: "compact-utc-micros"},
		WorkingRoot:     WorkingRoot{StationSegment: "{station_id}", TaskSegment: "task-{start}-{created}", RunSegment: "run-{created}", CollisionSuffix: "-{short_run_id}"},
		Filenames:       Filenames{AllowedCharset: "A-Z a-z 0-9 - _ .", ReplaceInvalid: "_", MaxSegmentLength: 128, StationCase: "preserve"},
	}
}

func DefaultIntegrity() Integrity {
	return Integrity{ChecksumPolicy: ChecksumAvailableOnly, AllowedAlgorithms: []string{"sha256"}, RecordSizeWhenAvailable: true, RecordMTimeWhenAvailable: true}
}

func (n Naming) WithDefaults() Naming {
	d := DefaultNaming()
	if n.TimestampFormat.TaskWindow == "" {
		n.TimestampFormat.TaskWindow = d.TimestampFormat.TaskWindow
	}
	if n.TimestampFormat.RuntimeEvent == "" {
		n.TimestampFormat.RuntimeEvent = d.TimestampFormat.RuntimeEvent
	}
	if n.WorkingRoot.StationSegment == "" {
		n.WorkingRoot.StationSegment = d.WorkingRoot.StationSegment
	}
	if n.WorkingRoot.TaskSegment == "" {
		n.WorkingRoot.TaskSegment = d.WorkingRoot.TaskSegment
	}
	if n.WorkingRoot.RunSegment == "" {
		n.WorkingRoot.RunSegment = d.WorkingRoot.RunSegment
	}
	if n.WorkingRoot.CollisionSuffix == "" {
		n.WorkingRoot.CollisionSuffix = d.WorkingRoot.CollisionSuffix
	}
	if n.Filenames.ReplaceInvalid == "" {
		n.Filenames.ReplaceInvalid = d.Filenames.ReplaceInvalid
	}
	if n.Filenames.AllowedCharset == "" {
		n.Filenames.AllowedCharset = d.Filenames.AllowedCharset
	}
	if n.Filenames.MaxSegmentLength <= 0 {
		n.Filenames.MaxSegmentLength = d.Filenames.MaxSegmentLength
	}
	if n.Filenames.StationCase == "" {
		n.Filenames.StationCase = d.Filenames.StationCase
	}
	return n
}

func EffectiveFilenamePattern(instancePattern, overridePattern, fileType string) string {
	pattern := strings.TrimSpace(instancePattern)
	if strings.TrimSpace(overridePattern) != "" {
		pattern = strings.TrimSpace(overridePattern)
	}
	if pattern == "" {
		return ""
	}
	return strings.ReplaceAll(pattern, "<FILE_TYPE>", fileType)
}

func ParseFilename(name, pattern string, components map[string]ComponentRule) (map[string]string, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("filename pattern is empty")
	}
	re, names, err := compileFilenamePattern(pattern, components)
	if err != nil {
		return nil, err
	}
	matches := re.FindStringSubmatch(name)
	if matches == nil {
		return nil, fmt.Errorf("filename %q does not match pattern %q", name, pattern)
	}
	parsed := map[string]string{}
	for idx, component := range names {
		value := matches[idx+1]
		rule := components[component]
		if err := validateComponentValue(component, value, rule); err != nil {
			return nil, err
		}
		parsed[componentJSONName(component)] = value
	}
	return parsed, nil
}

func ParseFilenameTime(value string) (time.Time, error) {
	if t, err := time.Parse("20060102T150405Z", value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("20060102T150405", value); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("timestamp %q does not match YYYYMMDDTHHmmSS[Z] format", value)
}

func compileFilenamePattern(pattern string, components map[string]ComponentRule) (*regexp.Regexp, []string, error) {
	var expr strings.Builder
	names := []string{}
	expr.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '<':
			end := strings.IndexByte(pattern[i:], '>')
			if end < 0 {
				return nil, nil, fmt.Errorf("unterminated filename component in %q", pattern)
			}
			component := pattern[i+1 : i+end]
			rule, ok := components[component]
			if !ok {
				return nil, nil, fmt.Errorf("filename component %q has no rule", component)
			}
			part, err := componentRegexp(rule)
			if err != nil {
				return nil, nil, fmt.Errorf("component %s: %w", component, err)
			}
			expr.WriteString("(")
			expr.WriteString(part)
			expr.WriteString(")")
			names = append(names, component)
			i += end + 1
		case '*':
			expr.WriteString(".*")
			i++
		case '?':
			expr.WriteString(".")
			i++
		default:
			expr.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	expr.WriteString("$")
	re, err := regexp.Compile(expr.String())
	if err != nil {
		return nil, nil, err
	}
	return re, names, nil
}

func componentRegexp(rule ComponentRule) (string, error) {
	if rule.Pattern != "" {
		var expr strings.Builder
		for _, r := range rule.Pattern {
			if r == '?' {
				expr.WriteString("[\\x00-\\x7F]")
				continue
			}
			expr.WriteString(regexp.QuoteMeta(string(r)))
		}
		return expr.String(), nil
	}
	switch strings.ToUpper(rule.Format) {
	case "YYYYMMDDTHHMMSSZ":
		return `[0-9]{8}T[0-9]{6}Z`, nil
	case "YYYYMMDDTHHMMSS":
		return `[0-9]{8}T[0-9]{6}`, nil
	}
	if rule.Length <= 0 {
		return "", fmt.Errorf("component length must be positive")
	}
	switch strings.ToLower(rule.Charset) {
	case "", "ascii":
		return fmt.Sprintf(`[\x00-\x7F]{%d}`, rule.Length), nil
	default:
		return fmt.Sprintf(`.{%d}`, rule.Length), nil
	}
}

func validateComponentValue(component, value string, rule ComponentRule) error {
	switch strings.ToUpper(rule.Format) {
	case "YYYYMMDDTHHMMSSZ":
		if _, err := time.Parse("20060102T150405Z", value); err != nil {
			return fmt.Errorf("component %s timestamp: %w", component, err)
		}
		return nil
	case "YYYYMMDDTHHMMSS":
		if _, err := time.Parse("20060102T150405", value); err != nil {
			return fmt.Errorf("component %s timestamp: %w", component, err)
		}
		return nil
	}
	if rule.Length > 0 && len(value) != rule.Length {
		return fmt.Errorf("component %s length = %d, want %d", component, len(value), rule.Length)
	}
	return nil
}

func componentJSONName(name string) string {
	return strings.ToLower(name)
}

func (i Integrity) WithDefaults() Integrity {
	d := DefaultIntegrity()
	if i.ChecksumPolicy == "" {
		i.ChecksumPolicy = d.ChecksumPolicy
	}
	if len(i.AllowedAlgorithms) == 0 {
		i.AllowedAlgorithms = d.AllowedAlgorithms
	}
	if !i.RecordSizeWhenAvailable {
		i.RecordSizeWhenAvailable = d.RecordSizeWhenAvailable
	}
	if !i.RecordMTimeWhenAvailable {
		i.RecordMTimeWhenAvailable = d.RecordMTimeWhenAvailable
	}
	return i
}

func CompactTaskWindow(t time.Time) string {
	u := t.UTC()
	return u.Format("20060102T150405") + milli(u) + "Z"
}

func CompactRuntimeEvent(t time.Time) string {
	u := t.UTC()
	return u.Format("20060102T150405") + micro(u) + "Z"
}

func milli(t time.Time) string { return t.Format(".000")[1:] }

func micro(t time.Time) string { return t.Format(".000000")[1:] }

func GenerateTaskID(n Naming, start, created time.Time) string {
	n = n.WithDefaults()
	segment := expand(n.WorkingRoot.TaskSegment, map[string]string{
		"start":   CompactTaskWindow(start),
		"created": CompactRuntimeEvent(created),
	})
	return NormalizeSegment(segment, n)
}

func ExpandWorkingRootSegment(template string, values map[string]string, n Naming) string {
	return NormalizeSegment(expand(template, values), n)
}

func NormalizeSegment(s string, n Naming) string {
	n = n.WithDefaults()
	repl := n.Filenames.ReplaceInvalid
	if repl == "" {
		repl = "_"
	}
	s = strings.TrimSpace(s)
	s = invalidSegmentChars.ReplaceAllString(s, repl)
	s = strings.Trim(s, repl)
	if s == "" {
		s = "unnamed"
	}
	if n.Filenames.MaxSegmentLength > 0 && len(s) > n.Filenames.MaxSegmentLength {
		s = s[:n.Filenames.MaxSegmentLength]
	}
	return s
}

func ShortRunID(runID string) string {
	clean := invalidSegmentChars.ReplaceAllString(runID, "")
	if len(clean) <= 12 {
		return clean
	}
	return clean[len(clean)-12:]
}

func expand(template string, values map[string]string) string {
	out := template
	for k, v := range values {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

var invalidSegmentChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
