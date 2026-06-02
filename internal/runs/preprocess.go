package runs

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// inputTokenPattern matches {input:FILE_TYPE} tokens in preprocess_args.
// The file type may contain any characters except '}'.
var inputTokenPattern = regexp.MustCompile(`\{input:([^}]+)\}`)

// runPreprocessScript executes the station's preprocess_script and returns the
// parsed KEY=VALUE variables. It is called before joborder rendering.
//
// The script path and each arg element are resolved through the station context
// (supporting <name> and <name.path> references). Arg elements may also contain
// {input:FILE_TYPE} tokens that expand to the space-joined paths of all present
// resolved inputs of the named file type.
//
// On success the script must write zero or more KEY=VALUE lines to stdout.
// On non-zero exit, unresolvable tokens, or unparseable output the run fails.
//
// The function always writes logs/preprocess_script.log under the working root
// (creating logs/ if needed) and emits structured lines to logger.
func runPreprocessScript(
	scriptPath string,
	rawArgs []string,
	runCtx map[string]any,
	manifest *store.ManifestRecord,
	run *store.RunRecord,
	absolutePaths bool,
	logger zerolog.Logger,
) (map[string]string, error) {
	// Resolve the script path via the station context.
	resolvedScriptRaw, err := stations.ResolveString(scriptPath, runCtx)
	if err != nil {
		return nil, fmt.Errorf("resolve preprocess_script path %q: %w", scriptPath, err)
	}
	resolvedScript, ok := resolvedScriptRaw.(string)
	if !ok || resolvedScript == "" {
		return nil, fmt.Errorf("preprocess_script path resolved to a non-string or empty value")
	}

	// Build per-file-type path index from the frozen manifest.
	inputsByType := buildPreprocessInputsByType(manifest, run, absolutePaths)

	// Expand {input:FILE_TYPE} tokens and resolve context refs in each arg.
	resolvedArgs, err := resolvePreprocessArgs(rawArgs, runCtx, inputsByType)
	if err != nil {
		return nil, fmt.Errorf("preprocess_args: %w", err)
	}

	// Log the command that is about to run.
	cmdLine := shellCommand(resolvedScript, resolvedArgs)
	logger.Info().
		Str("script", resolvedScript).
		Str("command", cmdLine).
		Msg("joborder preprocess_script: running")

	// Execute the script.
	cmd := exec.Command(resolvedScript, resolvedArgs...) // #nosec G204 – operator-configured script
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Always persist stdout + stderr into the run's logs directory.
	writePreprocessLog(run.WorkingRoot, cmdLine, stdout.String(), stderr.String(), runErr)

	if runErr != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("preprocess_script %q failed: %w\nstderr: %s", resolvedScript, runErr, stderrStr)
		}
		return nil, fmt.Errorf("preprocess_script %q failed: %w", resolvedScript, runErr)
	}

	// Parse KEY=VALUE output.
	vars, err := parsePreprocessOutput(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("preprocess_script %q: parse output: %w", resolvedScript, err)
	}

	// Log the result.
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	logger.Info().
		Str("script", resolvedScript).
		Int("vars", len(vars)).
		Strs("keys", keys).
		Msg("joborder preprocess_script: completed")

	return vars, nil
}

// buildPreprocessInputsByType indexes all present manifest entries by file type,
// converting each path to absolute or relative form.
func buildPreprocessInputsByType(manifest *store.ManifestRecord, run *store.RunRecord, absolutePaths bool) map[string][]string {
	index := make(map[string][]string)
	for _, entry := range manifest.Entries {
		if !entry.Present || entry.Path == "" {
			continue
		}
		var path string
		if absolutePaths {
			path = filepath.ToSlash(filepath.Join(run.WorkingRoot, filepath.FromSlash(entry.Path)))
		} else {
			path = "./" + filepath.ToSlash(entry.Path)
		}
		index[entry.FileType] = append(index[entry.FileType], path)
	}
	return index
}

// resolvePreprocessArgs expands {input:FILE_TYPE} tokens and station context
// references in each raw arg element.
func resolvePreprocessArgs(rawArgs []string, runCtx map[string]any, inputsByType map[string][]string) ([]string, error) {
	if len(rawArgs) == 0 {
		return nil, nil
	}
	expanded := make([]string, 0, len(rawArgs))
	for i, arg := range rawArgs {
		// First expand {input:FILE_TYPE} tokens (may contain spaces).
		withInputs, err := expandInputTokens(arg, inputsByType)
		if err != nil {
			return nil, fmt.Errorf("[%d] %q: %w", i, arg, err)
		}
		expanded = append(expanded, withInputs)
	}
	// Then resolve station context references (<name>) in each element.
	return stations.ResolveArgs(expanded, runCtx)
}

// expandInputTokens replaces every {input:FILE_TYPE} occurrence within arg
// with the space-joined paths of all present inputs of that type.
// It returns an error when a referenced file type has no present paths.
func expandInputTokens(arg string, inputsByType map[string][]string) (string, error) {
	var expandErr error
	result := inputTokenPattern.ReplaceAllStringFunc(arg, func(match string) string {
		if expandErr != nil {
			return match
		}
		sub := inputTokenPattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		fileType := sub[1]
		paths, ok := inputsByType[fileType]
		if !ok || len(paths) == 0 {
			expandErr = fmt.Errorf("{input:%s}: no present input paths for file type %q", fileType, fileType)
			return match
		}
		return strings.Join(paths, " ")
	})
	if expandErr != nil {
		return "", expandErr
	}
	return result, nil
}

// parsePreprocessOutput parses the KEY=VALUE line format produced by the
// preprocess script. Blank lines and lines beginning with '#' are ignored.
// Any other line that does not match KEY=VALUE is an error.
func parsePreprocessOutput(output string) (map[string]string, error) {
	vars := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(output))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", lineNum, line)
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key in %q", lineNum, line)
		}
		value := line[idx+1:]
		vars[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}
	return vars, nil
}

// injectPrepVars merges prepVars into ctx under the "prep" key so that
// station context references of the form <prep.KEY> resolve to the
// corresponding script output values.
func injectPrepVars(ctx map[string]any, prepVars map[string]string) {
	if len(prepVars) == 0 {
		return
	}
	prep := make(map[string]any, len(prepVars))
	for k, v := range prepVars {
		prep[k] = v
	}
	ctx["prep"] = prep
}

// shellCommand formats a command line string for display in logs.
func shellCommand(script string, args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, script)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}

// writePreprocessLog writes a structured log file for the preprocess script
// invocation to <workingRoot>/logs/preprocess_script.log. Errors are silently
// ignored — this is diagnostic output and must not mask the real run result.
func writePreprocessLog(workingRoot, cmdLine, stdout, stderr string, runErr error) {
	logsDir := filepath.Join(workingRoot, "logs")
	_ = os.MkdirAll(logsDir, 0o755)

	var b strings.Builder
	b.WriteString("=== VeriProc preprocess_script ===\n")
	b.WriteString("command: ")
	b.WriteString(cmdLine)
	b.WriteString("\n")
	if runErr != nil {
		b.WriteString("exit:    ")
		b.WriteString(runErr.Error())
		b.WriteString("\n")
	} else {
		b.WriteString("exit:    0\n")
	}
	b.WriteString("\n--- stdout ---\n")
	b.WriteString(stdout)
	if stdout != "" && !strings.HasSuffix(stdout, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n--- stderr ---\n")
	b.WriteString(stderr)
	if stderr != "" && !strings.HasSuffix(stderr, "\n") {
		b.WriteString("\n")
	}

	logPath := filepath.Join(logsDir, "preprocess_script.log")
	_ = os.WriteFile(logPath, []byte(b.String()), 0o644)
}
