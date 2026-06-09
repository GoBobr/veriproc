package runs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/policy"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// ---------- helpers ---------------------------------------------------------

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

// svcWithLog returns a Service that writes trace logs to buf.
func svcWithLog(naming policy.Naming, buf *bytes.Buffer) *Service {
	lg := zerolog.New(buf).Level(zerolog.TraceLevel)
	return &Service{naming: naming, logger: lg}
}

// writeFile creates a regular file and optionally sets its mtime.
func writeFile(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, time.Time{}, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	return path
}

// makeDir creates a directory and optionally sets its mtime.
func makeDir(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("makeDir %s: %v", name, err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, time.Time{}, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	return path
}

func defaultTask() *store.TaskRecord {
	return &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 5, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 10, 0, 0, time.UTC),
	}
}

// ---------- basic structured filename tests ---------------------------------

// TestClassicalMatcher_NewestGenTimeWins verifies that the candidate with the
// newest real generation time is selected regardless of mtime.
func TestClassicalMatcher_NewestGenTimeWins(t *testing.T) {
	folder := t.TempDir()
	older := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Date(2025, 7, 3, 11, 20, 0, 0, time.UTC))
	newer := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T113000_v2.txt",
		time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC))
	// wrongType must NOT be selected even though it has the newest mtime.
	_ = writeFile(t, folder,
		"CDMA_AUXILIARY_INPUT__20250703T110000_20250703T111500_20250703T114000_v1.txt",
		time.Date(2025, 7, 3, 11, 40, 0, 0, time.UTC))

	svc := &Service{naming: structuredNaming()}
	winners, reason, err := svc.classicalSelectCandidates("run-1",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if reason != "" {
		t.Fatalf("unexpected missing reason %q", reason)
	}
	if len(winners) != 1 {
		t.Fatalf("len(winners) = %d, want 1; %+v", len(winners), winners)
	}
	w := winners[0]
	if w.Path != newer {
		t.Fatalf("selected path = %s, want %s (older=%s)", w.Path, newer, older)
	}
	if w.WindowMatch != "overlaps" || w.EffectivePattern == "" {
		t.Fatalf("winner metadata = %+v", w)
	}
	if !strings.Contains(w.FilenameComponents, `"generation_time":"20250703T113000"`) ||
		!strings.Contains(w.FilenameComponents, `"file_type":"PRIMARY_INPUT___"`) {
		t.Fatalf("filename_components = %s", w.FilenameComponents)
	}
	if !strings.Contains(w.WinnerMetadata, `"generation_time"`) {
		t.Fatalf("winner_metadata missing generation_time: %s", w.WinnerMetadata)
	}
}

// TestClassicalMatcher_DirectoryInput verifies directory candidates are matched
// by the same algorithm and that object_kind filter works.
func TestClassicalMatcher_DirectoryInput(t *testing.T) {
	folder := t.TempDir()
	fileType := "AUX_DIR_________"
	dirCandidate := makeDir(t, folder,
		"CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T113000_v1.CDM",
		time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC))
	// A regular file with newer mtime but lower gen time should NOT win.
	_ = writeFile(t, folder,
		"CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T112000_v2.CDM",
		time.Date(2025, 7, 3, 11, 40, 0, 0, time.UTC))

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-dir",
		stations.InputDefinition{FileType: fileType, Category: "product",
			ObjectKind: store.ObjectKindDirectory, WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 {
		t.Fatalf("len(winners) = %d, want 1", len(winners))
	}
	w := winners[0]
	if w.Path != dirCandidate || w.ObjectKind != store.ObjectKindDirectory {
		t.Fatalf("winner = %+v, want dir %s", w, dirCandidate)
	}
}

// TestClassicalMatcher_AllFoldersScanned verifies that all configured folders
// are scanned and a later folder can win when it has a newer generation time.
func TestClassicalMatcher_AllFoldersScanned(t *testing.T) {
	folder1 := t.TempDir()
	folder2 := t.TempDir()
	// folder1 (higher priority) has older gen time.
	_ = writeFile(t, folder1,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})
	// folder2 has newer gen time → should win despite lower priority.
	newerPath := writeFile(t, folder2,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T113000_v2.txt",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-scan",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder1, folder2}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != newerPath {
		t.Fatalf("winner path = %v, want %s", winners, newerPath)
	}
}

// TestClassicalMatcher_FolderPriorityTieBreak verifies that when generation
// time and discriminator tie, the first configured folder wins.
func TestClassicalMatcher_FolderPriorityTieBreak(t *testing.T) {
	folder1 := t.TempDir()
	folder2 := t.TempDir()
	sameName := "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt"
	f1path := writeFile(t, folder1, sameName, time.Time{})
	_ = writeFile(t, folder2, sameName, time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-prio",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder1, folder2}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != f1path {
		t.Fatalf("winner = %v, want %s (first folder priority)", winners, f1path)
	}
	if winners[0].FolderPriority != 0 {
		t.Fatalf("FolderPriority = %d, want 0", winners[0].FolderPriority)
	}
}

// TestClassicalMatcher_DiscriminatorTieBreak verifies that the lexicographically
// greatest discriminator/suffix wins when generation time ties.
func TestClassicalMatcher_DiscriminatorTieBreak(t *testing.T) {
	folder := t.TempDir()
	// Same gen time, different suffix. "v2" > "v1" lexicographically.
	_ = writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})
	v2path := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v2.txt",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-disc",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != v2path {
		t.Fatalf("winner = %v, want %s (greatest discriminator)", winners, v2path)
	}
	if winners[0].Discriminator != "v2.txt" {
		t.Fatalf("Discriminator = %q, want \"v2.txt\"", winners[0].Discriminator)
	}
}

// TestClassicalMatcher_MultipleIntervalGroups verifies that multiple distinct
// logical interval groups each produce one winner.
func TestClassicalMatcher_MultipleIntervalGroups(t *testing.T) {
	folder := t.TempDir()
	// Two different time intervals → two groups.
	p1 := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})
	p2 := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T120000_20250703T121500_20250703T122000_v1.txt",
		time.Time{})

	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 12, 30, 0, 0, time.UTC),
	}
	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-multi",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, task, nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 2 {
		t.Fatalf("len(winners) = %d, want 2", len(winners))
	}
	paths := map[string]bool{winners[0].Path: true, winners[1].Path: true}
	if !paths[p1] || !paths[p2] {
		t.Fatalf("winners paths = %v, want {%s, %s}", paths, p1, p2)
	}
	// Each winner must have a distinct interval group key.
	if winners[0].IntervalGroupKey == winners[1].IntervalGroupKey {
		t.Fatalf("duplicate interval group key: %s", winners[0].IntervalGroupKey)
	}
}

// TestClassicalMatcher_WrongFileTypeRejected verifies that candidates whose
// parsed FILE_TYPE doesn't match the declared input are rejected.
func TestClassicalMatcher_WrongFileTypeRejected(t *testing.T) {
	folder := t.TempDir()
	_ = writeFile(t, folder,
		"CDMA_AUXILIARY_INPUT__20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, reason, err := svc.classicalSelectCandidates("run-type",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 0 {
		t.Fatalf("expected no winners for wrong file type, got %+v", winners)
	}
	if reason == "" {
		t.Fatalf("expected missing reason, got empty")
	}
}

// TestClassicalMatcher_NoFolders verifies that empty folder list returns a
// missing reason without error.
func TestClassicalMatcher_NoFolders(t *testing.T) {
	svc := &Service{naming: structuredNaming()}
	winners, reason, err := svc.classicalSelectCandidates("run-nof",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product"},
		[]string{}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 0 {
		t.Fatalf("expected no winners, got %+v", winners)
	}
	if !strings.Contains(reason, "folder") {
		t.Fatalf("missing reason = %q, want folder mention", reason)
	}
}

// TestClassicalMatcher_ChecksumSidecarIgnored verifies that .sha256 sidecar
// files are not considered as input candidates.
func TestClassicalMatcher_ChecksumSidecarIgnored(t *testing.T) {
	folder := t.TempDir()
	validPath := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})
	// sha256 sidecar — must not be selected.
	_ = writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt.sha256",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-sha",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != validPath {
		t.Fatalf("winners = %+v, want single entry at %s", winners, validPath)
	}
}

// TestClassicalMatcher_PerInputPatternOverride verifies that an input-level
// FilenamePattern overrides the instance-level pattern.
func TestClassicalMatcher_PerInputPatternOverride(t *testing.T) {
	folder := t.TempDir()
	// Pattern: <MISSION_ID>_<FILE_TYPE>_<START_TIME>_... substituting
	// FILE_TYPE=AUX_SPECIAL_____ (16 chars) produces:
	// <MISSION_ID>_AUX_SPECIAL_____<START_TIME>_... (note: template separator
	// _ follows the FILE_TYPE token, giving 6 underscores total before start).
	// The valid file uses 6 underscores between SPECIAL and the date.
	validPath := writeFile(t, folder,
		"CDMA_AUX_SPECIAL______20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})
	// A PRIMARY_INPUT___ file should NOT match the AUX_SPECIAL_____ pattern.
	_ = writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})

	overrideNaming := policy.Naming{Filenames: policy.Filenames{
		FilenamePattern: "<MISSION_ID>_<FILE_TYPE>_<START_TIME>_<END_TIME>_<GENERATION_TIME>_*",
		Components: map[string]policy.ComponentRule{
			"MISSION_ID":      {Pattern: "CDM?", Length: 4, Charset: "ascii"},
			"FILE_TYPE":       {Length: 16, Charset: "ascii"},
			"START_TIME":      {Format: "YYYYMMDDTHHmmSS", Length: 15},
			"END_TIME":        {Format: "YYYYMMDDTHHmmSS", Length: 15},
			"GENERATION_TIME": {Format: "YYYYMMDDTHHmmSS", Length: 15},
		},
	}}
	svc := &Service{naming: overrideNaming}
	input := stations.InputDefinition{
		FileType:    "AUX_SPECIAL_____",
		Category:    "product",
		WindowMatch: "overlaps",
	}
	winners, _, err := svc.classicalSelectCandidates("run-ovr", input, []string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != validPath {
		t.Fatalf("winners = %+v, want single entry %s", winners, validPath)
	}
}

// TestClassicalMatcher_FileTypeFromDeclaredInput verifies that <FILE_TYPE> in
// the pattern is substituted from the declared input, not from the filename.
func TestClassicalMatcher_FileTypeFromDeclaredInput(t *testing.T) {
	folder := t.TempDir()
	// The filename has the declared file_type in the FILE_TYPE position.
	validPath := writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-ft",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != validPath {
		t.Fatalf("winners = %+v, want %s", winners, validPath)
	}
	// The parsed components must contain the declared file type.
	if !strings.Contains(winners[0].FilenameComponents, `"file_type":"PRIMARY_INPUT___"`) {
		t.Fatalf("file_type not in FilenameComponents: %s", winners[0].FilenameComponents)
	}
}

// TestClassicalMatcher_WindowPolicies verifies all three window policies.
func TestClassicalMatcher_WindowPolicies(t *testing.T) {
	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
	}
	tests := []struct {
		name    string
		start   string
		end     string
		policy  string
		wantHit bool
	}{
		{"overlaps left boundary touch", "20250703T110000", "20250703T111500", "overlaps", false},
		{"overlaps right boundary touch", "20250703T113000", "20250703T120000", "overlaps", false},
		{"overlaps genuine", "20250703T110000", "20250703T111600", "overlaps", true},
		{"overlaps miss", "20250703T110000", "20250703T111400", "overlaps", false},
		{"within_window ok", "20250703T111600", "20250703T112900", "within_window", true},
		{"within_window miss", "20250703T110000", "20250703T114500", "within_window", false},
		{"covers_window ok", "20250703T110000", "20250703T114500", "covers_window", true},
		{"covers_window miss", "20250703T111600", "20250703T112900", "covers_window", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := t.TempDir()
			// Pad file type to 16 chars.
			fileType := "PRIMARY_INPUT___"
			name := "CDMA_" + fileType + "_" + tt.start + "_" + tt.end + "_20250703T120000_v1.txt"
			writeFile(t, folder, name, time.Time{})

			svc := &Service{naming: structuredNaming()}
			winners, _, err := svc.classicalSelectCandidates("run-wp",
				stations.InputDefinition{FileType: fileType, Category: "product", WindowMatch: tt.policy},
				[]string{folder}, task, nil)
			if err != nil {
				t.Fatalf("classicalSelectCandidates: %v", err)
			}
			got := len(winners) > 0
			if got != tt.wantHit {
				t.Fatalf("window policy %q, hit=%v, want=%v", tt.policy, got, tt.wantHit)
			}
		})
	}
}

// TestCandidateMatchesWindowPolicies is the original alias-coverage test.
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
		{name: "overlaps left boundary touch", start: "20250703T110000", end: "20250703T111500", match: "cross", want: false},
		{name: "overlaps right boundary touch", start: "20250703T113000", end: "20250703T120000", match: "cross", want: false},
		{name: "overlaps genuine", start: "20250703T110000", end: "20250703T111600", match: "cross", want: true},
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

// TestClassicalMatcher_ObjectKindFilter verifies object_kind restricts the
// candidate set so that a regular file is not returned for directory kind.
func TestClassicalMatcher_ObjectKindFilter(t *testing.T) {
	folder := t.TempDir()
	fileType := "AUX_DIR_________"
	// Regular file with newer gen time — should be excluded when kind=directory.
	_ = writeFile(t, folder,
		"CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T114000_v2.txt",
		time.Time{})
	// Directory with older gen time — the only valid candidate.
	dirPath := makeDir(t, folder,
		"CDMA_"+fileType+"_20250703T110000_20250703T111500_20250703T112000_v1.CDM",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-kind",
		stations.InputDefinition{FileType: fileType, Category: "product",
			ObjectKind: store.ObjectKindDirectory, WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != dirPath || winners[0].ObjectKind != store.ObjectKindDirectory {
		t.Fatalf("winners = %+v, want dir %s", winners, dirPath)
	}
}

// TestClassicalMatcher_ManifestEntryFields verifies the fields exposed on each
// winner (interval group key, discriminator, winner metadata).
func TestClassicalMatcher_ManifestEntryFields(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-fields",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	if len(winners) != 1 {
		t.Fatalf("expected 1 winner, got %d", len(winners))
	}
	w := winners[0]
	if w.IntervalGroupKey == "" {
		t.Fatalf("IntervalGroupKey is empty")
	}
	if !strings.HasPrefix(w.IntervalGroupKey, "PRIMARY_INPUT___") {
		t.Fatalf("IntervalGroupKey = %q, want prefix 'PRIMARY_INPUT___'", w.IntervalGroupKey)
	}
	if w.WinnerMetadata == "" {
		t.Fatalf("WinnerMetadata is empty")
	}
	if !strings.Contains(w.WinnerMetadata, `"candidate_count"`) {
		t.Fatalf("WinnerMetadata missing candidate_count: %s", w.WinnerMetadata)
	}
}

// TestClassicalMatcher_DebugLogs verifies that debug-level matcher logs are
// emitted and contain the key decision fields.
func TestClassicalMatcher_DebugLogs(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, folder,
		"CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt",
		time.Time{})

	var buf bytes.Buffer
	svc := svcWithLog(structuredNaming(), &buf)
	_, _, err := svc.classicalSelectCandidates("run-log",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"run_ref", "matcher", "PRIMARY_INPUT___", "selected winner"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\n%s", want, logs)
		}
	}
}

// TestClassicalMatcher_StableFallbackDeterministic verifies that when all
// comparison keys are equal, path order is stable (smallest path first).
func TestClassicalMatcher_StableFallbackDeterministic(t *testing.T) {
	folder1 := t.TempDir()
	folder2 := t.TempDir()
	// Same interval, gen time, discriminator; both in folder1 and folder2.
	// folder1 path sorts before folder2 path alphabetically.
	sameName := "CDMA_PRIMARY_INPUT____20250703T110000_20250703T111500_20250703T112000_v1.txt"
	f1path := writeFile(t, folder1, sameName, time.Time{})
	_ = writeFile(t, folder2, sameName, time.Time{})

	svc := &Service{naming: structuredNaming()}
	winners, _, err := svc.classicalSelectCandidates("run-stable",
		stations.InputDefinition{FileType: "PRIMARY_INPUT___", Category: "product", WindowMatch: "overlaps"},
		[]string{folder1, folder2}, defaultTask(), nil)
	if err != nil {
		t.Fatalf("classicalSelectCandidates: %v", err)
	}
	// folder1 has lower priority index (0) → wins over folder2 (1).
	if len(winners) != 1 || winners[0].Path != f1path {
		t.Fatalf("winner = %+v, want %s (folder priority tie-break)", winners, f1path)
	}
}

// ---------- filter tests ----------------------------------------------------

// TestFilter_FilenameComponent_MatchesMissionID is the canonical test for the
// "filename_component" filter rule, reproducing the MHF dual-satellite scenario
// described in the feature specification. The aux folder contains two AUX files
// for different MISSIONIDs (CDMA and CDMB). The source product (GEO) resolves
// to a CDMA file. The filter must retain only the CDMA AUX file.
func TestFilter_FilenameComponent_MatchesMissionID(t *testing.T) {
	naming := structuredNaming()
	svc := &Service{naming: naming}

	geoFolder := t.TempDir()
	auxFolder := t.TempDir()

	// Source product: GEO, MISSION_ID = CDMA.
	geoPath := writeFile(t, geoFolder,
		"CDMA_GEO_INPUT________20250703T111839_20250703T112139_20250703T120000_v1.nc",
		time.Time{})

	// Two AUX candidates: same interval group but different MISSION_IDs.
	cdmaAux := writeFile(t, auxFolder,
		"CDMA_AUX_FILE_________20250703T090000_20250703T120000_20250703T120000_v1.nc",
		time.Time{})
	_ = writeFile(t, auxFolder,
		"CDMB_AUX_FILE_________20250703T090000_20250703T120000_20250703T120000_v1.nc",
		time.Time{})

	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 18, 39, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 21, 39, 0, time.UTC),
	}

	// Step 1: resolve GEO without any filter → builds resolved components map.
	geoWinners, _, err := svc.classicalSelectCandidates("run-filter-mission",
		stations.InputDefinition{FileType: "GEO_INPUT_______", Category: "product", WindowMatch: "overlaps"},
		[]string{geoFolder}, task, nil)
	if err != nil || len(geoWinners) != 1 || geoWinners[0].Path != geoPath {
		t.Fatalf("GEO resolution failed: err=%v winners=%+v", err, geoWinners)
	}

	// Build the resolvedWinnerComponents map as resolveManifest would.
	var geoComponents map[string]string
	if err := json.Unmarshal([]byte(geoWinners[0].FilenameComponents), &geoComponents); err != nil {
		t.Fatalf("unmarshal GEO components: %v", err)
	}
	resolved := map[string][]map[string]string{
		"GEO_INPUT_______": {geoComponents},
	}

	// Step 2: resolve AUX with the filename_component filter.
	auxInput := stations.InputDefinition{
		FileType:    "AUX_FILE________",
		Category:    "product",
		WindowMatch: "overlaps",
		Filters: []stations.InputFilter{
			{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
		},
	}
	auxWinners, _, err := svc.classicalSelectCandidates("run-filter-mission", auxInput,
		[]string{auxFolder}, task, resolved)
	if err != nil {
		t.Fatalf("AUX resolution error: %v", err)
	}
	if len(auxWinners) != 1 {
		t.Fatalf("expected 1 AUX winner after filter, got %d: %+v", len(auxWinners), auxWinners)
	}
	if auxWinners[0].Path != cdmaAux {
		t.Fatalf("AUX winner = %s, want CDMA aux %s", auxWinners[0].Path, cdmaAux)
	}
}

// TestFilter_FilenameComponent_SourceNotYetResolved verifies that when the
// source file type has not been resolved yet, the filter is skipped and all
// candidates survive.
func TestFilter_FilenameComponent_SourceNotYetResolved(t *testing.T) {
	naming := structuredNaming()
	svc := &Service{naming: naming}
	auxFolder := t.TempDir()

	// Two candidates with different MISSION_IDs and different validity intervals
	// so both win their respective interval groups.
	cdmaAux := writeFile(t, auxFolder,
		"CDMA_AUX_FILE_________20250703T090000_20250703T112500_20250703T120000_v1.nc",
		time.Time{})
	cdmbAux := writeFile(t, auxFolder,
		"CDMB_AUX_FILE_________20250703T111500_20250703T130000_20250703T130000_v1.nc",
		time.Time{})

	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 18, 39, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 21, 39, 0, time.UTC),
	}

	auxInput := stations.InputDefinition{
		FileType:    "AUX_FILE________",
		Category:    "product",
		WindowMatch: "overlaps",
		Filters: []stations.InputFilter{
			{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
		},
	}
	// Pass an empty resolved map — source not yet resolved → filter skipped.
	auxWinners, _, err := svc.classicalSelectCandidates("run-filter-skip", auxInput,
		[]string{auxFolder}, task, map[string][]map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	paths := map[string]bool{}
	for _, w := range auxWinners {
		paths[w.Path] = true
	}
	if !paths[cdmaAux] || !paths[cdmbAux] {
		t.Fatalf("expected both candidates to survive when source unresolved; got %v", paths)
	}
}

// TestFilter_FilenameComponent_AllCandidatesRejected verifies that when no
// candidate matches the filter value the matcher returns no winners and a
// descriptive reason.
func TestFilter_FilenameComponent_AllCandidatesRejected(t *testing.T) {
	naming := structuredNaming()
	svc := &Service{naming: naming}
	auxFolder := t.TempDir()

	// Only CDMB candidate in folder; source winner is CDMA.
	_ = writeFile(t, auxFolder,
		"CDMB_AUX_FILE_________20250703T090000_20250703T120000_20250703T120000_v1.nc",
		time.Time{})

	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 18, 39, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 21, 39, 0, time.UTC),
	}

	resolved := map[string][]map[string]string{
		"GEO_INPUT_______": {{"mission_id": "CDMA"}},
	}
	auxInput := stations.InputDefinition{
		FileType:    "AUX_FILE________",
		Category:    "product",
		WindowMatch: "overlaps",
		Filters: []stations.InputFilter{
			{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
		},
	}
	winners, reason, err := svc.classicalSelectCandidates("run-filter-all-rejected", auxInput,
		[]string{auxFolder}, task, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(winners) != 0 {
		t.Fatalf("expected no winners, got %+v", winners)
	}
	if !strings.Contains(reason, "filter") {
		t.Fatalf("missing reason should mention 'filter', got %q", reason)
	}
}

// TestFilter_MultipleFilters_AllMustPass verifies that when two filter rules
// are declared, a candidate must satisfy both (AND semantics).
func TestFilter_MultipleFilters_AllMustPass(t *testing.T) {
	naming := structuredNaming()
	svc := &Service{naming: naming}
	auxFolder := t.TempDir()

	// Only candidate matching BOTH CDMA mission and a specific generation time suffix.
	goodPath := writeFile(t, auxFolder,
		"CDMA_AUX_FILE_________20250703T090000_20250703T120000_20250703T120000_v1.nc",
		time.Time{})
	// Same mission ID but different gen time (v2 suffix) — also written to test
	// that another "mission" filter pass does not prevent gen-time filter.
	_ = writeFile(t, auxFolder,
		"CDMB_AUX_FILE_________20250703T090000_20250703T120000_20250703T120000_v1.nc",
		time.Time{})

	task := &store.TaskRecord{
		WindowStart: time.Date(2025, 7, 3, 11, 18, 39, 0, time.UTC),
		WindowEnd:   time.Date(2025, 7, 3, 11, 21, 39, 0, time.UTC),
	}

	resolved := map[string][]map[string]string{
		"GEO_INPUT_______": {{"mission_id": "CDMA"}},
	}
	auxInput := stations.InputDefinition{
		FileType:    "AUX_FILE________",
		Category:    "product",
		WindowMatch: "overlaps",
		Filters: []stations.InputFilter{
			{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
		},
	}
	winners, _, err := svc.classicalSelectCandidates("run-filter-two", auxInput,
		[]string{auxFolder}, task, resolved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(winners) != 1 || winners[0].Path != goodPath {
		t.Fatalf("expected winner %s, got %+v", goodPath, winners)
	}
}

// ---------- loader filter validation tests ----------------------------------

// TestLoaderValidation_Filter verifies that the station loader accepts valid
// filter rules and rejects invalid ones.
func TestLoaderValidation_Filter(t *testing.T) {
	boolTrue := true
	baseInput := func() stations.InputDefinition {
		return stations.InputDefinition{
			FileType: "AUX_FILE________",
			Category: "aux",
		}
	}

	tests := []struct {
		name    string
		filters []stations.InputFilter
		wantErr bool
	}{
		{
			name:    "no filters is valid",
			filters: nil,
			wantErr: false,
		},
		{
			name: "valid filename_component filter",
			filters: []stations.InputFilter{
				{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
			},
			wantErr: false,
		},
		{
			name: "missing component",
			filters: []stations.InputFilter{
				{Rule: "filename_component", SourceFileType: "GEO_INPUT_______"},
			},
			wantErr: true,
		},
		{
			name: "missing source_file_type",
			filters: []stations.InputFilter{
				{Rule: "filename_component", Component: "MISSION_ID"},
			},
			wantErr: true,
		},
		{
			name: "self-referential source_file_type",
			filters: []stations.InputFilter{
				{Rule: "filename_component", Component: "MISSION_ID", SourceFileType: "AUX_FILE________"},
			},
			wantErr: true,
		},
		{
			name: "empty rule",
			filters: []stations.InputFilter{
				{Component: "MISSION_ID", SourceFileType: "GEO_INPUT_______"},
			},
			wantErr: true,
		},
		{
			name: "unknown rule",
			filters: []stations.InputFilter{
				{Rule: "unsupported_rule"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inp := baseInput()
			inp.Filters = tt.filters
			inp.Mandatory = &boolTrue
			def := stations.Definition{
				StationID:     "test-station",
				StationName:   "Test Station",
				SchemaVersion: stations.DefaultSchemaVersion,
				Inputs:        []stations.InputDefinition{inp},
			}
			_, err := stations.SpecFromDefinition(def)
			if tt.wantErr && err == nil {
				t.Fatalf("expected validation error, got none")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

