package runs

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/store"
)

// writeTestExecutable writes a shell script to dir and makes it executable.
func writeTestExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// parsePreprocessOutput
// ---------------------------------------------------------------------------

func TestParsePreprocessOutput_Empty(t *testing.T) {
	vars, err := parsePreprocessOutput("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected empty map, got %v", vars)
	}
}

func TestParsePreprocessOutput_KeyValue(t *testing.T) {
	output := "MIN_SCANLINE=6825\nMAX_SCANLINE=7444\nN_SCANLINES=620\nN_SCANLINES_MINUS_1=619\n"
	vars, err := parsePreprocessOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"MIN_SCANLINE":       "6825",
		"MAX_SCANLINE":       "7444",
		"N_SCANLINES":        "620",
		"N_SCANLINES_MINUS_1": "619",
	}
	if !maps.Equal(vars, want) {
		t.Errorf("vars = %v, want %v", vars, want)
	}
}

func TestParsePreprocessOutput_BlankLinesAndComments(t *testing.T) {
	output := "# header comment\n\nFOO=bar\n\n# trailing comment\nBAZ=qux\n"
	vars, err := parsePreprocessOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["FOO"] != "bar" || vars["BAZ"] != "qux" || len(vars) != 2 {
		t.Errorf("vars = %v", vars)
	}
}

func TestParsePreprocessOutput_ValueWithEquals(t *testing.T) {
	// Value itself contains '='.
	vars, err := parsePreprocessOutput("PATH=/usr/local/bin:/usr/bin\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["PATH"] != "/usr/local/bin:/usr/bin" {
		t.Errorf("PATH = %q, want /usr/local/bin:/usr/bin", vars["PATH"])
	}
}

func TestParsePreprocessOutput_EmptyValue(t *testing.T) {
	vars, err := parsePreprocessOutput("EMPTY=\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := vars["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q ok=%v, want empty string", v, ok)
	}
}

func TestParsePreprocessOutput_CRLFLineEndings(t *testing.T) {
	vars, err := parsePreprocessOutput("FOO=1\r\nBAR=2\r\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["FOO"] != "1" || vars["BAR"] != "2" {
		t.Errorf("vars = %v", vars)
	}
}

func TestParsePreprocessOutput_BadLine(t *testing.T) {
	_, err := parsePreprocessOutput("GOOD=ok\nthis-has-no-equals\n")
	if err == nil {
		t.Fatal("expected error for line without '='")
	}
	if !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Errorf("error message should mention KEY=VALUE format, got: %v", err)
	}
}

func TestParsePreprocessOutput_EmptyKey(t *testing.T) {
	_, err := parsePreprocessOutput("=value\n")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

// ---------------------------------------------------------------------------
// expandInputTokens
// ---------------------------------------------------------------------------

func TestExpandInputTokens_NoToken(t *testing.T) {
	result, err := expandInputTokens("--mode minmax", map[string][]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "--mode minmax" {
		t.Errorf("result = %q, want --mode minmax", result)
	}
}

func TestExpandInputTokens_SingleFile(t *testing.T) {
	inputs := map[string][]string{
		"SCE_2__CSM______": {"/data/input/file1.nc"},
	}
	result, err := expandInputTokens("{input:SCE_2__CSM______}", inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "/data/input/file1.nc" {
		t.Errorf("result = %q", result)
	}
}

func TestExpandInputTokens_MultipleFiles(t *testing.T) {
	inputs := map[string][]string{
		"SCE_2__CSM______": {"/data/input/file1.nc", "/data/input/file2.nc"},
	}
	result, err := expandInputTokens("{input:SCE_2__CSM______}", inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "/data/input/file1.nc /data/input/file2.nc" {
		t.Errorf("result = %q", result)
	}
}

func TestExpandInputTokens_EmbeddedInArg(t *testing.T) {
	inputs := map[string][]string{
		"FOO": {"/some/file.nc"},
	}
	result, err := expandInputTokens("prefix:{input:FOO}:suffix", inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "prefix:/some/file.nc:suffix" {
		t.Errorf("result = %q", result)
	}
}

func TestExpandInputTokens_UnknownFileType(t *testing.T) {
	_, err := expandInputTokens("{input:UNKNOWN_TYPE}", map[string][]string{})
	if err == nil {
		t.Fatal("expected error for unknown file type")
	}
	if !strings.Contains(err.Error(), "UNKNOWN_TYPE") {
		t.Errorf("error should mention file type, got: %v", err)
	}
}

func TestExpandInputTokens_MultipleTokens(t *testing.T) {
	inputs := map[string][]string{
		"A": {"/a.nc"},
		"B": {"/b.nc"},
	}
	result, err := expandInputTokens("{input:A} {input:B}", inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "/a.nc /b.nc" {
		t.Errorf("result = %q", result)
	}
}

// ---------------------------------------------------------------------------
// buildPreprocessInputsByType
// ---------------------------------------------------------------------------

func TestBuildPreprocessInputsByType_AbsolutePaths(t *testing.T) {
	manifest := &store.ManifestRecord{
		Entries: []store.ManifestEntry{
			{FileType: "TYPE_A", Path: "input/file_a.nc", Present: true},
			{FileType: "TYPE_B", Path: "input/file_b.nc", Present: true},
			{FileType: "TYPE_A", Path: "input/file_a2.nc", Present: true},
			{FileType: "TYPE_ABSENT", Path: "input/absent.nc", Present: false},
			{FileType: "TYPE_NOPATH", Path: "", Present: true},
		},
	}
	run := &store.RunRecord{WorkingRoot: "/work/root"}
	index := buildPreprocessInputsByType(manifest, run, true)

	if len(index["TYPE_A"]) != 2 {
		t.Errorf("TYPE_A count = %d, want 2", len(index["TYPE_A"]))
	}
	if index["TYPE_A"][0] != "/work/root/input/file_a.nc" {
		t.Errorf("TYPE_A[0] = %q", index["TYPE_A"][0])
	}
	if index["TYPE_A"][1] != "/work/root/input/file_a2.nc" {
		t.Errorf("TYPE_A[1] = %q", index["TYPE_A"][1])
	}
	if len(index["TYPE_B"]) != 1 || index["TYPE_B"][0] != "/work/root/input/file_b.nc" {
		t.Errorf("TYPE_B = %v", index["TYPE_B"])
	}
	if _, ok := index["TYPE_ABSENT"]; ok {
		t.Error("absent entry should not appear in index")
	}
	if _, ok := index["TYPE_NOPATH"]; ok {
		t.Error("empty-path entry should not appear in index")
	}
}

func TestBuildPreprocessInputsByType_RelativePaths(t *testing.T) {
	manifest := &store.ManifestRecord{
		Entries: []store.ManifestEntry{
			{FileType: "TYPE_A", Path: "input/file_a.nc", Present: true},
		},
	}
	run := &store.RunRecord{WorkingRoot: "/work/root"}
	index := buildPreprocessInputsByType(manifest, run, false)
	if index["TYPE_A"][0] != "./input/file_a.nc" {
		t.Errorf("relative path = %q, want ./input/file_a.nc", index["TYPE_A"][0])
	}
}

// ---------------------------------------------------------------------------
// injectPrepVars
// ---------------------------------------------------------------------------

func TestInjectPrepVars_AddsNamespace(t *testing.T) {
	ctx := map[string]any{"working_root": "/work"}
	injectPrepVars(ctx, map[string]string{"MIN_SCANLINE": "100", "MAX_SCANLINE": "200"})
	prep, ok := ctx["prep"].(map[string]any)
	if !ok {
		t.Fatalf("ctx[prep] = %T, want map[string]any", ctx["prep"])
	}
	if prep["MIN_SCANLINE"] != "100" || prep["MAX_SCANLINE"] != "200" {
		t.Errorf("prep = %v", prep)
	}
	// Original key must be untouched.
	if ctx["working_root"] != "/work" {
		t.Error("working_root should be unmodified")
	}
}

func TestInjectPrepVars_EmptyMapIsNoop(t *testing.T) {
	ctx := map[string]any{"working_root": "/work"}
	injectPrepVars(ctx, nil)
	if _, ok := ctx["prep"]; ok {
		t.Error("prep key should not be added for empty prepVars")
	}
	injectPrepVars(ctx, map[string]string{})
	if _, ok := ctx["prep"]; ok {
		t.Error("prep key should not be added for empty prepVars map")
	}
}

// ---------------------------------------------------------------------------
// runPreprocessScript (integration-style, uses real shell scripts)
// ---------------------------------------------------------------------------

func TestRunPreprocessScript_Success(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeTestExecutable(t, dir, "meta.sh", "#!/bin/sh\nprintf 'MIN_SCANLINE=100\nMAX_SCANLINE=200\n'\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{"working_root": dir}

	vars, err := runPreprocessScript(scriptPath, nil, runCtx, manifest, run, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["MIN_SCANLINE"] != "100" || vars["MAX_SCANLINE"] != "200" {
		t.Errorf("vars = %v", vars)
	}
}

func TestRunPreprocessScript_NonZeroExitFails(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeTestExecutable(t, dir, "fail.sh", "#!/bin/sh\necho 'something went wrong' >&2\nexit 1\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{}

	_, err := runPreprocessScript(scriptPath, nil, runCtx, manifest, run, true, zerolog.Nop())
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error should mention 'failed', got: %v", err)
	}
}

func TestRunPreprocessScript_BadOutputFails(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeTestExecutable(t, dir, "bad.sh", "#!/bin/sh\nprintf 'not-key-value-format\n'\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{}

	_, err := runPreprocessScript(scriptPath, nil, runCtx, manifest, run, true, zerolog.Nop())
	if err == nil {
		t.Fatal("expected error for unparseable output")
	}
	if !strings.Contains(err.Error(), "parse output") {
		t.Errorf("error should mention 'parse output', got: %v", err)
	}
}

func TestRunPreprocessScript_InputTokenExpansion(t *testing.T) {
	dir := t.TempDir()
	// Script that echoes its first argument as INPUT_PATH=<value>.
	scriptPath := writeTestExecutable(t, dir, "echo.sh", "#!/bin/sh\nprintf 'INPUT_PATH=%s\n' \"$1\"\n")

	manifest := &store.ManifestRecord{
		Entries: []store.ManifestEntry{
			{FileType: "MY_TYPE", Path: "input/myfile.nc", Present: true},
		},
	}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{"working_root": dir}

	vars, err := runPreprocessScript(scriptPath, []string{"{input:MY_TYPE}"}, runCtx, manifest, run, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPath := dir + "/input/myfile.nc"
	if vars["INPUT_PATH"] != wantPath {
		t.Errorf("INPUT_PATH = %q, want %q", vars["INPUT_PATH"], wantPath)
	}
}

func TestRunPreprocessScript_ContextRefInScript(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeTestExecutable(t, dir, "ctx.sh", "#!/bin/sh\nprintf 'WR=%s\n' \"$1\"\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{"working_root": dir}

	// Arg uses a context reference <working_root>.
	vars, err := runPreprocessScript(scriptPath, []string{"<working_root>"}, runCtx, manifest, run, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["WR"] != dir {
		t.Errorf("WR = %q, want %q", vars["WR"], dir)
	}
}

func TestRunPreprocessScript_PathContextRefInScript(t *testing.T) {
	dir := t.TempDir()
	// Script path itself uses a context reference.
	writeTestExecutable(t, dir, "path.sh", "#!/bin/sh\nprintf 'OK=1\n'\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	// The script path contains an embedded context reference.
	runCtx := map[string]any{"scripts_dir": dir}
	scriptTemplate := "<scripts_dir>/path.sh"

	vars, err := runPreprocessScript(scriptTemplate, nil, runCtx, manifest, run, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["OK"] != "1" {
		t.Errorf("vars = %v", vars)
	}
}

func TestRunPreprocessScript_EmptyOutput(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeTestExecutable(t, dir, "empty.sh", "#!/bin/sh\n# no output\n")

	manifest := &store.ManifestRecord{}
	run := &store.RunRecord{WorkingRoot: dir}
	runCtx := map[string]any{}

	vars, err := runPreprocessScript(scriptPath, nil, runCtx, manifest, run, true, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error for empty output: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected empty vars, got %v", vars)
	}
}
