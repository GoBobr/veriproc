package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

func TestSelectInputCandidateStructuredFilename(t *testing.T) {
	folder := t.TempDir()
	older := filepath.Join(folder, "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt")
	newer := filepath.Join(folder, "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T113000_v2.txt")
	wrongType := filepath.Join(folder, "CDMA_AUXILIARY_INPUT__20250703T110000_20250703T111500_20250703T114000_v1.txt")
	for _, path := range []string{older, newer, wrongType} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	if err := os.Chtimes(older, time.Time{}, time.Date(2025, 7, 3, 11, 20, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes older: %v", err)
	}
	if err := os.Chtimes(newer, time.Time{}, time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes newer: %v", err)
	}
	if err := os.Chtimes(wrongType, time.Time{}, time.Date(2025, 7, 3, 11, 40, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes wrong type: %v", err)
	}

	svc := &Service{naming: structuredNaming()}
	selected, err := svc.selectInputCandidate(stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"}, []string{folder}, &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 5, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 10, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("selectInputCandidate: %v", err)
	}
	if selected.Path != newer {
		t.Fatalf("selected path = %s, want %s", selected.Path, newer)
	}
	if selected.WindowMatch != "overlaps" || selected.EffectivePattern == "" {
		t.Fatalf("selected metadata = %#v", selected)
	}
	if !strings.Contains(selected.FilenameComponents, `"generation_time":"20250703T113000"`) || !strings.Contains(selected.FilenameComponents, `"file_type":"PRIMARY_INPUT___"`) {
		t.Fatalf("filename components = %s", selected.FilenameComponents)
	}
}

func TestSelectInputCandidateStructuredDirectory(t *testing.T) {
	folder := t.TempDir()
	fileType := "AUX_DIR_________"
	dirCandidate := filepath.Join(folder, "CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T113000_v1.CDM")
	if err := os.MkdirAll(dirCandidate, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirCandidate, "data.nc"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write nested payload: %v", err)
	}
	fileCandidate := filepath.Join(folder, "CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T114000_v2.CDM")
	if err := os.WriteFile(fileCandidate, []byte("regular file with newer mtime"), 0o644); err != nil {
		t.Fatalf("write regular fixture: %v", err)
	}
	if err := os.Chtimes(dirCandidate, time.Time{}, time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes dir: %v", err)
	}
	if err := os.Chtimes(fileCandidate, time.Time{}, time.Date(2025, 7, 3, 11, 40, 0, 0, time.UTC)); err != nil {
		t.Fatalf("chtimes file: %v", err)
	}

	svc := &Service{naming: structuredNaming()}
	selected, err := svc.selectInputCandidate(stations.InputDefinition{FileType: fileType, Category: "product", ObjectKind: store.ObjectKindDirectory, WindowMatch: "overlaps"}, []string{folder}, &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 5, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 10, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("selectInputCandidate: %v", err)
	}
	if selected.Path != dirCandidate || selected.ObjectKind != store.ObjectKindDirectory {
		t.Fatalf("selected = %#v, want directory %s", selected, dirCandidate)
	}
}

func TestCandidateMatchesWindowPolicies(t *testing.T) {
	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
	}
	tests := []struct {
		name  string
		start string
		end   string
		match string
		want  bool
	}{
		{name: "overlaps boundary", start: "20250703T110000", end: "20250703T111500", match: "cross", want: true},
		{name: "within", start: "20250703T111600", end: "20250703T112900", match: "fully_within", want: true},
		{name: "covers", start: "20250703T110000", end: "20250703T114500", match: "surrender", want: true},
		{name: "not within", start: "20250703T110000", end: "20250703T114500", match: "within_window", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := candidateMatchesWindow(map[string]string{"start_time": tt.start, "end_time": tt.end}, tt.match, stations.InputDefinition{}, task)
			if got != tt.want {
				t.Fatalf("candidateMatchesWindow = %v, want %v", got, tt.want)
			}
		})
	}
}

func structuredNaming() policy.Naming {
	return policy.Naming{Filenames: policy.Filenames{
		FilenamePattern: "<MISSION_ID>_<FILE_TYPE>_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*",
		Components: map[string]policy.ComponentRule{
			"MISSION_ID":      {Pattern: "CDM?", Length: 4, Charset: "ascii"},
			"FILE_TYPE":       {Length: 16, Charset: "ascii"},
			"START_TIME":      {Format: "YYYYMMDDTHHmmSS", Length: 15},
			"END_TIME":        {Format: "YYYYMMDDTHHmmSS", Length: 15},
			"GENERATION_TIME": {Format: "YYYYMMDDTHHmmSS", Length: 15},
		},
	}}
}
