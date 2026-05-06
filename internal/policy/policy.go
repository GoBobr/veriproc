package policy

import (
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
	AllowedCharset   string `yaml:"allowed_charset"`
	ReplaceInvalid   string `yaml:"replace_invalid"`
	MaxSegmentLength int    `yaml:"max_segment_length"`
	StationCase      string `yaml:"station_case"`
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
	if n.Filenames.MaxSegmentLength <= 0 {
		n.Filenames.MaxSegmentLength = d.Filenames.MaxSegmentLength
	}
	if n.Filenames.StationCase == "" {
		n.Filenames.StationCase = d.Filenames.StationCase
	}
	return n
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
