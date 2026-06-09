// Command veriproc is the reference VeriProc CLI (Spec Ch. 6). It is a thin
// HTTP client that maps subcommands onto the v1 REST API. All state-changing
// operations go through the API; no local control-plane state is kept.
//
// Subcommands:
//
//	submit          POST   /api/v1/tasks
//	task get        GET    /api/v1/tasks/{id}
//	task list       GET    /api/v1/tasks
//	run get         GET    /api/v1/runs/{id}
//	run list        GET    /api/v1/runs
//	run jobs        GET    /api/v1/runs/{id}/jobs
//	artifact list   GET    /api/v1/runs/{id}/artifacts
//	logs            GET    /api/v1/runs/{id}/logs
//	cancel          POST   /api/v1/runs/{id}/cancel
//	promote         POST   /api/v1/runs/{id}/promote
//	group list      GET    /api/v1/groups
//	group get       GET    /api/v1/groups/{id}
//	group close     POST   /api/v1/groups/{id}/close//		station list    GET    /api/v1/stations
//		station summary GET    /api/v1/stations/summary
//		station pause   POST   /api/v1/stations/{id}/pause
//		station unpause POST   /api/v1/stations/{id}/unpause//	health          GET    /health
//	readiness       GET    /readiness
//	version         (local) optional --check-api hits /api/v1/health
//
// Configuration (precedence: flags > env > defaults):
//
//	--api-url   / VERIPROC_API_URL   (default: http://localhost:8080)
//	--token     / VERIPROC_TOKEN
//	--output    / VERIPROC_OUTPUT    (table|json|yaml; default: table)
//	--timeout   / VERIPROC_TIMEOUT   (default: 30s)
//
// Exit codes follow Spec §6.10.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gobobr/veriproc/internal/policy"
	"github.com/gobobr/veriproc/internal/version"
)

// Spec §6.10 exit codes.
const (
	ExitOK            = 0
	ExitGeneric       = 1
	ExitUsage         = 2
	ExitValidation    = 3
	ExitNotFound      = 4
	ExitConflict      = 5
	ExitAuth          = 6
	ExitUnavailable   = 7
	ExitIndeterminate = 8
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return ExitUsage
	}
	// Pre-parse global flags: every command tolerates --api-url / --token /
	// --output / --timeout in any position. We strip them first so subcommand
	// flag parsing can use the remaining args.
	cfg, rest, err := parseGlobals(args)
	if err != nil {
		fmt.Fprintln(stderr, "veriproc: "+err.Error())
		return ExitUsage
	}
	if len(rest) == 0 {
		printUsage(stderr)
		return ExitUsage
	}

	cmd, sub, tail := dispatch(rest)
	c := &client{
		baseURL: cfg.APIURL,
		token:   cfg.Token,
		http:    &http.Client{Timeout: cfg.Timeout},
		out:     cfg.Output,
		stdout:  stdout,
		stderr:  stderr,
		stdin:   os.Stdin,
	}

	switch cmd {
	case "help", "--help", "-h":
		printUsage(stdout)
		return ExitOK
	case "submit":
		return c.cmdSubmit(tail)
	case "task":
		return c.cmdTask(sub, tail)
	case "run":
		return c.cmdRun(sub, tail)
	case "artifact":
		return c.cmdArtifact(sub, tail)
	case "logs":
		return c.cmdLogs(tail)
	case "cancel":
		return c.cmdCancel(tail)
	case "promote":
		return c.cmdPromote(tail)
	case "group", "groups":
		return c.cmdGroup(sub, tail)
	case "station", "stations":
		return c.cmdStation(sub, tail)
	case "clean":
		return c.cmdClean(tail)
	case "health":
		return c.cmdHealth()
	case "readiness":
		return c.cmdReadiness()
	case "version":
		return c.cmdVersion(tail)
	default:
		fmt.Fprintf(stderr, "veriproc: unknown command %q\n", cmd)
		printUsage(stderr)
		return ExitUsage
	}
}

// --- configuration ----------------------------------------------------------

type globalCfg struct {
	APIURL  string
	Token   string
	Output  string
	Timeout time.Duration
}

func parseGlobals(args []string) (globalCfg, []string, error) {
	cfg := globalCfg{
		APIURL:  envOr("VERIPROC_API_URL", "http://localhost:8080"),
		Token:   os.Getenv("VERIPROC_TOKEN"),
		Output:  envOr("VERIPROC_OUTPUT", "table"),
		Timeout: 30 * time.Second,
	}
	if v := os.Getenv("VERIPROC_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Timeout = d
		}
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--api-url":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--api-url requires value")
			}
			cfg.APIURL = args[i]
		case strings.HasPrefix(a, "--api-url="):
			cfg.APIURL = strings.TrimPrefix(a, "--api-url=")
		case a == "--token":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--token requires value")
			}
			cfg.Token = args[i]
		case strings.HasPrefix(a, "--token="):
			cfg.Token = strings.TrimPrefix(a, "--token=")
		case a == "--output", a == "-o":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--output requires value")
			}
			cfg.Output = args[i]
		case strings.HasPrefix(a, "--output="):
			cfg.Output = strings.TrimPrefix(a, "--output=")
		case a == "--timeout":
			i++
			if i >= len(args) {
				return cfg, nil, errors.New("--timeout requires value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return cfg, nil, fmt.Errorf("invalid --timeout: %v", err)
			}
			cfg.Timeout = d
		default:
			out = append(out, a)
		}
	}
	switch cfg.Output {
	case "table", "json", "yaml":
	default:
		return cfg, nil, fmt.Errorf("unsupported --output %q (want table|json|yaml)", cfg.Output)
	}
	return cfg, out, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// dispatch splits "task get TASK …" into (group, sub, args). Single-word
// commands are returned as (cmd, "", args).
func dispatch(args []string) (cmd, sub string, rest []string) {
	cmd = args[0]
	switch cmd {
	case "task", "run", "artifact", "group", "groups", "station", "stations":
		if len(args) >= 2 {
			return cmd, args[1], args[2:]
		}
		return cmd, "", nil
	}
	return cmd, "", args[1:]
}

// --- HTTP client ------------------------------------------------------------

type client struct {
	baseURL string
	token   string
	http    *http.Client
	out     string // table|json|yaml
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
	Body       []byte
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

func (c *client) do(method, path string, body any) (map[string]any, []byte, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		ae := &apiError{StatusCode: resp.StatusCode, Body: raw}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		ae.Code = env.Error.Code
		ae.Message = env.Error.Message
		return nil, raw, ae
	}
	if len(raw) == 0 {
		return nil, raw, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, raw, nil
	}
	return m, raw, nil
}

func (c *client) reportErr(err error) int {
	fmt.Fprintln(c.stderr, "veriproc: "+err.Error())
	var ae *apiError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusBadRequest:
			return ExitValidation
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusConflict:
			return ExitConflict
		case http.StatusUnauthorized, http.StatusForbidden:
			return ExitAuth
		case http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusGatewayTimeout:
			return ExitUnavailable
		}
		return ExitGeneric
	}
	// Network / connection failures: indeterminate for state-changing,
	// unavailable otherwise. We pick unavailable; callers can use the
	// indeterminate exit code via specific helpers when needed.
	return ExitUnavailable
}

// --- output rendering -------------------------------------------------------

func (c *client) writeJSON(raw []byte) {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		buf, _ := json.MarshalIndent(v, "", "  ")
		c.stdout.Write(buf)
		c.stdout.Write([]byte("\n"))
		return
	}
	c.stdout.Write(raw)
}

func (c *client) writeYAML(m map[string]any) {
	emitYAML(c.stdout, m, "")
}

func emitYAML(w io.Writer, v any, indent string) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := t[k]
			switch val.(type) {
			case map[string]any, []any:
				fmt.Fprintf(w, "%s%s:\n", indent, k)
				emitYAML(w, val, indent+"  ")
			default:
				fmt.Fprintf(w, "%s%s: %s\n", indent, k, scalar(val))
			}
		}
	case []any:
		for _, item := range t {
			switch item.(type) {
			case map[string]any, []any:
				fmt.Fprintf(w, "%s-\n", indent)
				emitYAML(w, item, indent+"  ")
			default:
				fmt.Fprintf(w, "%s- %s\n", indent, scalar(item))
			}
		}
	default:
		fmt.Fprintf(w, "%s%s\n", indent, scalar(t))
	}
}

func scalar(v any) string {
	if v == nil {
		return "null"
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		buf, _ := json.Marshal(v)
		return string(buf)
	}
}

// renderTable writes a tab-aligned table for a list of map[string]any items.
func (c *client) renderTable(cols []string, items []map[string]any) {
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers(cols), "\t"))
	for _, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = formatCell(it[c])
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
}

func headers(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = strings.ToUpper(strings.ReplaceAll(c, "_", " "))
	}
	return out
}

func formatCell(v any) string {
	if v == nil {
		return "-"
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "-"
		}
		if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return ts.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		buf, _ := json.Marshal(v)
		return string(buf)
	}
}

// renderResource picks a renderer based on c.out.
func (c *client) renderResource(raw []byte, m map[string]any, tableCols []string) {
	switch c.out {
	case "json":
		c.writeJSON(raw)
	case "yaml":
		c.writeYAML(m)
	default:
		// Table for single resource = key/value pairs of important fields.
		tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
		for _, k := range tableCols {
			if v, ok := m[k]; ok {
				fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(k), formatCell(v))
			}
		}
		tw.Flush()
	}
}

func (c *client) renderList(raw []byte, m map[string]any, tableCols []string) {
	switch c.out {
	case "json":
		c.writeJSON(raw)
	case "yaml":
		c.writeYAML(m)
	default:
		items, _ := m["items"].([]any)
		mapped := make([]map[string]any, 0, len(items))
		for _, it := range items {
			if mp, ok := it.(map[string]any); ok {
				mapped = append(mapped, mp)
			}
		}
		c.renderTable(tableCols, mapped)
		if next, _ := m["next_cursor"].(string); next != "" {
			fmt.Fprintf(c.stderr, "(more results: --cursor %s)\n", next)
		}
	}
}

// --- subcommand: submit -----------------------------------------------------

func (c *client) cmdSubmit(args []string) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	station := fs.String("station", "", "destination station id")
	start := fs.String("start", "", "window start (RFC3339)")
	end := fs.String("end", "", "window end (RFC3339)")
	idem := fs.String("idempotency-key", "", "Idempotency-Key")
	force := fs.Bool("force", false, "force flag")
	priority := fs.String("priority", "", "priority")
	splitGroup := fs.String("split-group", "", "split_group_id")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *station == "" {
		fmt.Fprintln(c.stderr, "veriproc submit: --station is required")
		return ExitUsage
	}
	if *start == "" || *end == "" {
		fmt.Fprintln(c.stderr, "veriproc submit: --start and --end are required")
		return ExitUsage
	}
	// Validate timestamp formats locally for fast feedback (Spec §6.5.1).
	if _, err := policy.ParseWindowTimestamp(*start); err != nil {
		fmt.Fprintf(c.stderr, "veriproc submit: --start: %v\n", err)
		return ExitValidation
	}
	if _, err := policy.ParseWindowTimestamp(*end); err != nil {
		fmt.Fprintf(c.stderr, "veriproc submit: --end: %v\n", err)
		return ExitValidation
	}
	body := map[string]any{
		"destination": map[string]any{
			"station_id": *station,
		},
		"window": map[string]any{
			"start": *start,
			"end":   *end,
		},
	}
	if *force {
		body["force"] = true
	}
	if *priority != "" {
		body["priority"] = *priority
	}
	if *idem != "" {
		body["idempotency_key"] = *idem
	}
	if *splitGroup != "" {
		body["split_group_id"] = *splitGroup
	}
	m, raw, err := c.do(http.MethodPost, "/api/v1/tasks", body)
	if err != nil {
		// On network error before response we cannot tell whether the task
		// was created → indeterminate.
		var ae *apiError
		if !errors.As(err, &ae) {
			fmt.Fprintln(c.stderr, "veriproc submit: "+err.Error()+" (outcome indeterminate)")
			return ExitIndeterminate
		}
		return c.reportErr(err)
	}
	task, _ := m["task"].(map[string]any)
	c.renderResource(raw, task,
		[]string{"task_id", "station_id", "state", "start", "end", "split_group_id", "failure_summary"})
	return ExitOK
}

// --- subcommand: task -------------------------------------------------------

func (c *client) cmdTask(sub string, args []string) int {
	switch sub {
	case "get":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc task get TASK_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodGet, "/api/v1/tasks/"+args[0], nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"task_id", "station_id", "start", "end", "state", "failure_summary", "latest_retry_index", "latest_run_ref", "canonical_retry_index", "canonical_run_ref", "split_group_id", "created_at"})
		return ExitOK
	case "list":
		q := buildQuery(args, []string{"station_id", "state", "split_group_id", "parent_task_id", "limit", "cursor"},
			map[string]string{"station": "station_id", "split_group": "split_group_id"})
		m, raw, err := c.do(http.MethodGet, "/api/v1/tasks"+q, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"task_id", "station_id", "start", "end", "state", "latest_run_ref", "created_at"})
		return ExitOK
	case "retry":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc task retry TASK_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodPost, "/api/v1/tasks/"+args[0]+"/retry", nil)
		if err != nil {
			var ae *apiError
			if !errors.As(err, &ae) {
				fmt.Fprintln(c.stderr, "veriproc task retry: "+err.Error()+" (outcome indeterminate)")
				return ExitIndeterminate
			}
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"run_ref", "task_id", "retry_index", "state", "created_at"})
		return ExitOK
	case "delete":
		fs := flag.NewFlagSet("task delete", flag.ContinueOnError)
		fs.SetOutput(c.stderr)
		force := fs.Bool("force", false, "skip the confirmation prompt")
		dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
		quiet := fs.Bool("quiet", false, "suppress the pre-delete content listing")
		if err := fs.Parse(args); err != nil {
			return ExitUsage
		}
		rest := fs.Args()
		if len(rest) < 1 {
			fmt.Fprintln(c.stderr, "veriproc task delete [--dry-run] [--force] [--quiet] TASK_ID [TASK_ID...]")
			return ExitUsage
		}
		paths := make([]string, len(rest))
		for i, id := range rest {
			paths[i] = "/api/v1/tasks/" + id
		}
		return c.cmdDelete("task", paths, rest, *dryRun, *force, *quiet)
	default:
		fmt.Fprintln(c.stderr, "veriproc task {get|list|retry|delete}")
		return ExitUsage
	}
}

// --- subcommand: run --------------------------------------------------------

// resolveRunID converts an operator-facing run locator to an internal run_id.
// It accepts either a raw run_id (e.g. "run-019e078e-...") or a composite
// task-scoped locator of the form TASK_ID/rN (e.g. "task-abc/r2").
func (c *client) resolveRunID(arg string) (string, error) {
	if idx := strings.LastIndex(arg, "/r"); idx > 0 {
		tail := arg[idx+2:]
		if allDigits(tail) {
			taskID := arg[:idx]
			retryIndex := tail
			m, _, err := c.do(http.MethodGet, "/api/v1/runs?task_id="+taskID+"&limit=100", nil)
			if err != nil {
				return "", err
			}
			items, _ := m["items"].([]any)
			for _, it := range items {
				run, ok := it.(map[string]any)
				if !ok {
					continue
				}
				ri := formatCell(run["retry_index"])
				if ri == retryIndex {
					if id, ok := run["run_id"].(string); ok && id != "" {
						return id, nil
					}
				}
			}
			return "", &apiError{StatusCode: 404, Code: "not_found", Message: "run " + arg + " not found"}
		}
	}
	return arg, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *client) cmdRun(sub string, args []string) int {
	switch sub {
	case "get":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc run get TASK_ID/rN")
			return ExitUsage
		}
		runID, err := c.resolveRunID(args[0])
		if err != nil {
			return c.reportErr(err)
		}
		m, raw, err := c.do(http.MethodGet, "/api/v1/runs/"+runID, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"station_id", "run_ref", "task_id", "start", "end", "state", "canonicality", "executor_type", "execution_node", "failure_reason", "created_at", "working_root"})
		return ExitOK
	case "list":
		q := buildQuery(args, []string{"task_id", "state", "canonicality", "station_id", "limit", "cursor"},
			map[string]string{"task": "task_id", "station": "station_id"})
		m, raw, err := c.do(http.MethodGet, "/api/v1/runs"+q, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"run_ref", "executor_type", "execution_node", "working_root", "state", "failure_reason", "created_at"})
		return ExitOK
	case "jobs":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc run jobs TASK_ID/rN")
			return ExitUsage
		}
		runID, err := c.resolveRunID(args[0])
		if err != nil {
			return c.reportErr(err)
		}
		m, raw, err := c.do(http.MethodGet, "/api/v1/runs/"+runID+"/jobs", nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"job_id", "executor_type", "scheduler_native_state", "execution_node", "submitted_at"})
		return ExitOK
	case "delete":
		fs := flag.NewFlagSet("run delete", flag.ContinueOnError)
		fs.SetOutput(c.stderr)
		force := fs.Bool("force", false, "skip the confirmation prompt")
		dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
		quiet := fs.Bool("quiet", false, "suppress the pre-delete content listing")
		if err := fs.Parse(args); err != nil {
			return ExitUsage
		}
		rest := fs.Args()
		if len(rest) < 1 {
			fmt.Fprintln(c.stderr, "veriproc run delete [--dry-run] [--force] [--quiet] TASK_ID/rN [TASK_ID/rN...]")
			return ExitUsage
		}
		paths := make([]string, 0, len(rest))
		for _, ref := range rest {
			runID, err := c.resolveRunID(ref)
			if err != nil {
				return c.reportErr(err)
			}
			paths = append(paths, "/api/v1/runs/"+runID)
		}
		return c.cmdDelete("run", paths, rest, *dryRun, *force, *quiet)
	default:
		fmt.Fprintln(c.stderr, "veriproc run {get|list|jobs|delete}")
		return ExitUsage
	}
}

// --- subcommand: clean / delete --------------------------------------------

// cleanReport mirrors the JSON envelope returned by the cleaner endpoints
// (internal/cleaner.Report). Only the fields the CLI renders are decoded.
type cleanReport struct {
	DryRun              bool           `json:"dry_run"`
	Counts              map[string]int `json:"counts"`
	TaskIDs             []string       `json:"task_ids"`
	RunIDs              []string       `json:"run_ids"`
	WorkingRoots        []string       `json:"working_roots"`
	WorkingRootsRemoved int            `json:"working_roots_removed"`
	FilesystemErrors    []string       `json:"filesystem_errors"`
}

// countOrder fixes the display order of the per-table deletion counts.
var countOrder = []string{
	"tasks", "runs", "jobs", "artifacts", "publications",
	"manifests", "manifest_entries", "deduplication_records",
	"canonicality_audits", "split_group_members", "task_history_entries",
	"provenance_links", "idempotency_records",
}

// renderCleanReport prints a human-readable summary of a cleanup report. In
// json/yaml output mode it defers to the raw renderers instead.
// When quiet is true only the counts table is printed; the per-ID and
// working-root path listings are suppressed.
func (c *client) renderCleanReport(rep *cleanReport, raw []byte, quiet bool) {
	if c.out == "json" {
		c.writeJSON(raw)
		return
	}
	if c.out == "yaml" {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			c.writeYAML(m)
			return
		}
	}
	// Counts table — always shown.
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	// tasks and runs are derived from the ID slices (always populated) so they
	// appear even when the API omits them from the counts map.
	if n := len(rep.TaskIDs); n > 0 {
		fmt.Fprintf(tw, "tasks\t%d\n", n)
	}
	if n := len(rep.RunIDs); n > 0 {
		fmt.Fprintf(tw, "runs\t%d\n", n)
	}
	for _, k := range countOrder {
		if k == "tasks" || k == "runs" {
			continue // already emitted above
		}
		if v, ok := rep.Counts[k]; ok && v > 0 {
			fmt.Fprintf(tw, "%s\t%d\n", k, v)
		}
	}
	// Working-root count — always shown as a summary line.
	if len(rep.WorkingRoots) > 0 {
		if rep.DryRun {
			fmt.Fprintf(tw, "working roots to remove\t%d\n", len(rep.WorkingRoots))
		} else {
			fmt.Fprintf(tw, "working roots removed\t%d/%d\n", rep.WorkingRootsRemoved, len(rep.WorkingRoots))
		}
	}
	tw.Flush()
	if quiet {
		// Suppress per-ID listings in quiet mode.
		for _, fe := range rep.FilesystemErrors {
			fmt.Fprintln(c.stderr, "veriproc: filesystem error: "+fe)
		}
		return
	}
	// Task IDs.
	if len(rep.TaskIDs) > 0 {
		fmt.Fprintln(c.stdout, "\ntask ids:")
		for _, id := range rep.TaskIDs {
			fmt.Fprintf(c.stdout, "  %s\n", id)
		}
	}
	// Run IDs.
	if len(rep.RunIDs) > 0 {
		fmt.Fprintln(c.stdout, "\nrun ids:")
		for _, id := range rep.RunIDs {
			fmt.Fprintf(c.stdout, "  %s\n", id)
		}
	}
	// Working root paths.
	if len(rep.WorkingRoots) > 0 {
		if rep.DryRun {
			fmt.Fprintf(c.stdout, "\nworking roots to remove (%d):\n", len(rep.WorkingRoots))
		} else {
			fmt.Fprintf(c.stdout, "\nworking roots removed (%d/%d):\n", rep.WorkingRootsRemoved, len(rep.WorkingRoots))
		}
		for _, wr := range rep.WorkingRoots {
			fmt.Fprintf(c.stdout, "  %s\n", wr)
		}
	}
	for _, fe := range rep.FilesystemErrors {
		fmt.Fprintln(c.stderr, "veriproc: filesystem error: "+fe)
	}
}

// confirm reads a single line from stdin and returns true only for an
// affirmative answer (y / yes, case-insensitive).
func (c *client) confirm(prompt string) bool {
	fmt.Fprint(c.stdout, prompt)
	if c.stdin == nil {
		return false
	}
	reader := bufio.NewReader(c.stdin)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// mergeReports combines multiple cleanReports into one by summing counts and
// appending ID/path slices. DryRun is taken from the first report.
func mergeReports(reports []*cleanReport) *cleanReport {
	merged := &cleanReport{Counts: make(map[string]int)}
	for i, rep := range reports {
		if i == 0 {
			merged.DryRun = rep.DryRun
		}
		for k, v := range rep.Counts {
			merged.Counts[k] += v
		}
		merged.TaskIDs = append(merged.TaskIDs, rep.TaskIDs...)
		merged.RunIDs = append(merged.RunIDs, rep.RunIDs...)
		merged.WorkingRoots = append(merged.WorkingRoots, rep.WorkingRoots...)
		merged.WorkingRootsRemoved += rep.WorkingRootsRemoved
		merged.FilesystemErrors = append(merged.FilesystemErrors, rep.FilesystemErrors...)
	}
	return merged
}

// cmdDelete performs DELETE requests for one or more paths, with a combined
// content listing, an optional confirmation prompt, and optional dry-run.
// Always shows counts before prompting or deleting. Without --quiet the full
// per-ID and working-root path listing is also shown.
func (c *client) cmdDelete(label string, paths, refs []string, dryRun, force, quiet bool) int {
	// collectReports issues a DELETE (dry-run or real) against every path and
	// returns the merged report. It stops and returns an error on first failure.
	collectReports := func(dry bool) (*cleanReport, error) {
		reports := make([]*cleanReport, 0, len(paths))
		for _, p := range paths {
			rep, _, err := c.deleteCall(p, dry)
			if err != nil {
				return nil, err
			}
			reports = append(reports, rep)
		}
		return mergeReports(reports), nil
	}

	renderMerged := func(merged *cleanReport, dry bool) {
		merged.DryRun = dry
		raw, _ := json.Marshal(merged)
		c.renderCleanReport(merged, raw, quiet)
	}

	if dryRun {
		merged, err := collectReports(true)
		if err != nil {
			return c.reportErr(err)
		}
		renderMerged(merged, true)
		return ExitOK
	}

	// Always show a pre-delete preview (counts always; full listing unless --quiet).
	preview, err := collectReports(true)
	if err != nil {
		return c.reportErr(err)
	}
	renderMerged(preview, true)

	if !force {
		var prompt string
		if len(refs) == 1 {
			prompt = fmt.Sprintf("Delete %s %s and all related artifacts? [y/N] ", label, refs[0])
		} else {
			prompt = fmt.Sprintf("Delete %d %ss and all related artifacts? [y/N] ", len(refs), label)
		}
		if !c.confirm(prompt) {
			fmt.Fprintln(c.stderr, "veriproc: aborted")
			return ExitOK
		}
	}

	result, err := collectReports(false)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			fmt.Fprintln(c.stderr, "veriproc "+label+" delete: "+err.Error()+" (outcome indeterminate)")
			return ExitIndeterminate
		}
		return c.reportErr(err)
	}
	renderMerged(result, false)
	return ExitOK
}

// deleteCall issues the DELETE request, appending ?dry_run=true when requested,
// and decodes the cleanup report.
func (c *client) deleteCall(path string, dryRun bool) (*cleanReport, []byte, error) {
	if dryRun {
		path += "?dry_run=true"
	}
	_, raw, err := c.do(http.MethodDelete, path, nil)
	if err != nil {
		return nil, nil, err
	}
	var rep cleanReport
	_ = json.Unmarshal(raw, &rep)
	return &rep, raw, nil
}

// cmdClean implements `veriproc clean --before TS | --after TS [--dry-run]
// [--force] [--quiet]`. With neither cutoff it prints the command help (Spec §6).
func (c *client) cmdClean(args []string) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	before := fs.String("before", "", "delete tasks at or before this timestamp")
	after := fs.String("after", "", "delete tasks at or after this timestamp")
	by := fs.String("by", "processing-time", "cutoff basis: processing-time (created_at) or processing-window (sensing window)")
	force := fs.Bool("force", false, "skip the confirmation prompt")
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	quiet := fs.Bool("quiet", false, "suppress the pre-delete content listing")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *before == "" && *after == "" {
		printCleanHelp(c.stdout)
		return ExitOK
	}
	switch *by {
	case "processing-time", "processing-window":
	default:
		fmt.Fprintln(c.stderr, "veriproc clean: invalid --by: must be processing-time or processing-window")
		return ExitValidation
	}

	// Validate timestamps client-side for a friendly error before the round-trip.
	body := map[string]any{"basis": *by}
	if *before != "" {
		if _, err := policy.ParseWindowTimestamp(*before); err != nil {
			fmt.Fprintln(c.stderr, "veriproc clean: invalid --before: "+err.Error())
			return ExitValidation
		}
		body["before"] = *before
	}
	if *after != "" {
		if _, err := policy.ParseWindowTimestamp(*after); err != nil {
			fmt.Fprintln(c.stderr, "veriproc clean: invalid --after: "+err.Error())
			return ExitValidation
		}
		body["after"] = *after
	}

	if *dryRun {
		rep, raw, err := c.cleanCall(body, true)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderCleanReport(rep, raw, *quiet)
		return ExitOK
	}
	// Always show a pre-delete preview (counts always; full listing unless --quiet).
	previewRep, previewRaw, err := c.cleanCall(body, true)
	if err != nil {
		return c.reportErr(err)
	}
	c.renderCleanReport(previewRep, previewRaw, *quiet)
	if len(previewRep.TaskIDs) == 0 {
		fmt.Fprintln(c.stdout, "nothing to delete")
		return ExitOK
	}
	if !*force {
		if !c.confirm("Delete the listed tasks, runs, and artifacts? [y/N] ") {
			fmt.Fprintln(c.stderr, "veriproc: aborted")
			return ExitOK
		}
	}
	rep, raw, err := c.cleanCall(body, false)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			fmt.Fprintln(c.stderr, "veriproc clean: "+err.Error()+" (outcome indeterminate)")
			return ExitIndeterminate
		}
		return c.reportErr(err)
	}
	c.renderCleanReport(rep, raw, *quiet)
	return ExitOK
}

// cleanCall posts the clean request and decodes the cleanup report.
func (c *client) cleanCall(body map[string]any, dryRun bool) (*cleanReport, []byte, error) {
	payload := map[string]any{"dry_run": dryRun}
	for k, v := range body {
		payload[k] = v
	}
	_, raw, err := c.do(http.MethodPost, "/api/v1/maintenance/clean", payload)
	if err != nil {
		return nil, nil, err
	}
	var rep cleanReport
	_ = json.Unmarshal(raw, &rep)
	return &rep, raw, nil
}

// printCleanHelp documents the clean command when invoked without a cutoff.
func printCleanHelp(w io.Writer) {
	fmt.Fprint(w, `veriproc clean — delete tasks, runs, and on-disk artifacts within a time range.

Usage:
  veriproc clean --before TIMESTAMP [--by BASIS] [--dry-run] [--force] [--quiet]
  veriproc clean --after  TIMESTAMP [--by BASIS] [--dry-run] [--force] [--quiet]
  veriproc clean --after  T1 --before T2 [--by BASIS] [--dry-run] [--force] [--quiet]

Selection basis (--by, default processing-time):
  processing-time     compare against the task processing time (created_at)
    --before T   delete tasks created at or before T
    --after  T   delete tasks created at or after T
  processing-window   compare against the data sensing window
    --before T   delete tasks whose sensing window ends at or before T
    --after  T   delete tasks whose sensing window starts at or after T
  combining both selects tasks within [after, before]

Timestamps accept RFC 3339 (2025-05-29T10:00:00Z) or compact UTC
(20250529T100000) forms.

Options:
  --by BASIS   processing-time (default) or processing-window
  --dry-run    print what would be deleted, then exit without deleting
  --force      skip the interactive confirmation prompt
  --quiet      suppress the pre-delete content listing (combine with --force
               for fully non-interactive scripted deletion)

By default, clean always lists the affected tasks/runs/artifacts before
prompting for confirmation or deleting anything. Use --quiet to suppress
the listing (the interactive prompt and final result are still shown unless
--force is also given).
`)
}

// --- subcommand: artifact ---------------------------------------------------

func (c *client) cmdArtifact(sub string, args []string) int {
	switch sub {
	case "list":
		fs := flag.NewFlagSet("artifact list", flag.ContinueOnError)
		fs.SetOutput(c.stderr)
		runID := fs.String("run", "", "run locator (TASK_ID/rN or internal run_id)")
		logical := fs.String("type", "", "logical_type filter")
		if err := fs.Parse(args); err != nil {
			return ExitUsage
		}
		if *runID == "" {
			fmt.Fprintln(c.stderr, "veriproc artifact list --run TASK_ID/rN")
			return ExitUsage
		}
		resolvedID, err := c.resolveRunID(*runID)
		if err != nil {
			return c.reportErr(err)
		}
		path := "/api/v1/runs/" + resolvedID + "/artifacts"
		if *logical != "" {
			path += "?logical_type=" + *logical
		}
		m, raw, err := c.do(http.MethodGet, path, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"artifact_id", "logical_type", "object_kind", "availability", "size", "checksum_source", "created_at"})
		return ExitOK
	default:
		fmt.Fprintln(c.stderr, "veriproc artifact {list}")
		return ExitUsage
	}
}

// --- subcommand: logs / cancel / promote -----------------------------------

func (c *client) cmdLogs(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(c.stderr, "veriproc logs TASK_ID/rN")
		return ExitUsage
	}
	runID, err := c.resolveRunID(args[0])
	if err != nil {
		return c.reportErr(err)
	}
	m, raw, err := c.do(http.MethodGet, "/api/v1/runs/"+runID+"/logs", nil)
	if err != nil {
		return c.reportErr(err)
	}
	c.renderList(raw, m, []string{"artifact_id", "logical_type", "object_kind", "availability", "created_at"})
	return ExitOK
}

func (c *client) cmdCancel(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	yes := fs.Bool("yes", false, "skip confirmation")
	reason := fs.String("reason", "", "cancellation reason")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(c.stderr, "veriproc cancel [--yes] [--reason TEXT] TASK_ID/rN")
		return ExitUsage
	}
	if !*yes {
		fmt.Fprintln(c.stderr, "veriproc cancel: pass --yes to confirm cancellation")
		return ExitUsage
	}
	body := map[string]any{}
	if *reason != "" {
		body["reason"] = *reason
	}
	runID, err := c.resolveRunID(rest[0])
	if err != nil {
		return c.reportErr(err)
	}
	m, raw, err := c.do(http.MethodPost, "/api/v1/runs/"+runID+"/cancel", body)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			fmt.Fprintln(c.stderr, "veriproc cancel: "+err.Error()+" (outcome indeterminate)")
			return ExitIndeterminate
		}
		return c.reportErr(err)
	}
	c.renderResource(raw, m, []string{"state", "accepted", "cancellation_complete"})
	return ExitOK
}

func (c *client) cmdPromote(args []string) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	reason := fs.String("reason", "", "promotion reason (required)")
	actor := fs.String("actor", "", "actor (required)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(c.stderr, "veriproc promote --reason R --actor A TASK_ID/rN")
		return ExitUsage
	}
	if *reason == "" || *actor == "" {
		fmt.Fprintln(c.stderr, "veriproc promote: --reason and --actor are required")
		return ExitUsage
	}
	body := map[string]any{"reason": *reason, "actor": *actor}
	runID, err := c.resolveRunID(rest[0])
	if err != nil {
		return c.reportErr(err)
	}
	m, raw, err := c.do(http.MethodPost, "/api/v1/runs/"+runID+"/promote", body)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			fmt.Fprintln(c.stderr, "veriproc promote: "+err.Error()+" (outcome indeterminate)")
			return ExitIndeterminate
		}
		return c.reportErr(err)
	}
	c.renderResource(raw, m, []string{"canonicality"})
	return ExitOK
}

// --- subcommand: group ------------------------------------------------------

func (c *client) cmdGroup(sub string, args []string) int {
	switch sub {
	case "list":
		q := buildQuery(args, []string{"state", "limit"})
		m, raw, err := c.do(http.MethodGet, "/api/v1/groups"+q, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"split_group_id", "state", "member_count", "canonical_count", "failed_count", "created_at"})
		return ExitOK
	case "get":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc group get GROUP_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodGet, "/api/v1/groups/"+args[0], nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"split_group_id", "state", "member_count", "canonical_count", "failed_count", "summary", "created_at", "closed_at"})
		return ExitOK
	case "close":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc group close GROUP_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodPost, "/api/v1/groups/"+args[0]+"/close", nil)
		if err != nil {
			var ae *apiError
			if !errors.As(err, &ae) {
				fmt.Fprintln(c.stderr, "veriproc group close: "+err.Error()+" (outcome indeterminate)")
				return ExitIndeterminate
			}
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"split_group_id", "state", "member_count", "canonical_count", "failed_count", "summary", "closed_at"})
		return ExitOK
	default:
		fmt.Fprintln(c.stderr, "veriproc group {list|get|close}")
		return ExitUsage
	}
}

// --- subcommand: station ---------------------------------------------------

func (c *client) cmdStation(sub string, args []string) int {
	switch sub {
	case "list":
		m, raw, err := c.do(http.MethodGet, "/api/v1/stations", nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"station_id", "station_name", "paused"})
		return ExitOK

	case "summary":
		fs := flag.NewFlagSet("station summary", flag.ContinueOnError)
		fs.SetOutput(c.stderr)
		since := fs.String("since", "", "interval start (RFC3339, default 24h ago)")
		if err := fs.Parse(args); err != nil {
			return ExitUsage
		}
		rest := fs.Args()
		var path string
		singleStation := ""
		if len(rest) > 0 {
			singleStation = rest[0]
			path = "/api/v1/stations/" + singleStation + "/summary"
		} else {
			path = "/api/v1/stations/summary"
		}
		if *since != "" {
			path += "?since=" + *since
		}
		m, raw, err := c.do(http.MethodGet, path, nil)
		if err != nil {
			return c.reportErr(err)
		}
		switch c.out {
		case "json":
			c.writeJSON(raw)
		case "yaml":
			c.writeYAML(m)
		default:
			if singleStation != "" {
				if st, ok := m["station"].(map[string]any); ok {
					c.renderStationSummaryTable([]map[string]any{st})
				} else {
					c.writeJSON(raw)
				}
			} else {
				items, _ := m["items"].([]any)
				rows := make([]map[string]any, 0, len(items))
				for _, it := range items {
					if mp, ok := it.(map[string]any); ok {
						rows = append(rows, mp)
					}
				}
				c.renderStationSummaryTable(rows)
			}
		}
		return ExitOK

	case "pause":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc station pause STATION_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodPost, "/api/v1/stations/"+args[0]+"/pause", nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"station_id", "station_name", "paused", "running_count", "queued_count"})
		return ExitOK

	case "unpause":
		if len(args) < 1 {
			fmt.Fprintln(c.stderr, "veriproc station unpause STATION_ID")
			return ExitUsage
		}
		m, raw, err := c.do(http.MethodPost, "/api/v1/stations/"+args[0]+"/unpause", nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderResource(raw, m, []string{"station_id", "station_name", "paused", "running_count", "queued_count"})
		return ExitOK

	case "topology":
		return c.cmdStationTopology(args)

	default:
		fmt.Fprintln(c.stderr, "veriproc station {list|summary|pause|unpause|topology}")
		return ExitUsage
	}
}

// renderStationSummaryTable renders station summary items flattening the nested
// counts object into SUCCESS and FAILURE columns.
func (c *client) cmdStationTopology(args []string) int {
	fs := flag.NewFlagSet("topology", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	format := fs.String("format", "block", "output format: block, mermaid")
	byInput := fs.Bool("by-input", false, "show data-flow graph (outputs -> inputs)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	m, _, err := c.do(http.MethodGet, "/api/v1/stations", nil)
	if err != nil {
		return c.reportErr(err)
	}

	items, _ := m["items"].([]any)
	stations := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if mp, ok := it.(map[string]any); ok {
			stations = append(stations, mp)
		}
	}
	if len(stations) == 0 {
		fmt.Fprintln(c.stdout, "no stations configured")
		return ExitOK
	}

	nodesMap := make(map[string]topoNode, len(stations))
	var order []string
	for _, st := range stations {
		id, _ := st["station_id"].(string)
		name, _ := st["station_name"].(string)
		down, _ := st["downstream"].([]any)
		downIDs := make([]string, 0, len(down))
		for _, d := range down {
			if sid, ok := d.(string); ok && sid != "" {
				downIDs = append(downIDs, sid)
			}
		}
		paused, _ := st["paused"].(bool)
		if paused {
			if name == "" {
				name = "(paused)"
			} else {
				name = name + " (paused)"
			}
		}
		declInputs, _ := st["declared_inputs"].([]any)
		inputTypes := make([]string, 0, len(declInputs))
		for _, d := range declInputs {
			if s, ok := d.(string); ok && s != "" {
				inputTypes = append(inputTypes, s)
			}
		}
		declOutputs, _ := st["declared_outputs"].([]any)
		outputTypes := make([]string, 0, len(declOutputs))
		for _, d := range declOutputs {
			if s, ok := d.(string); ok && s != "" {
				outputTypes = append(outputTypes, s)
			}
		}
		nodesMap[id] = topoNode{
			id:          id,
			name:        name,
			paused:      paused,
			downstream:  downIDs,
			inputTypes:  inputTypes,
			outputTypes: outputTypes,
		}
		order = append(order, id)
	}

	if *byInput {
		switch strings.ToLower(*format) {
		case "mermaid":
			return c.renderTopologyInputMermaid(nodesMap, order)
		default:
			return c.renderTopologyInputBlock(nodesMap, order)
		}
	}
	switch strings.ToLower(*format) {
	case "mermaid":
		return c.renderTopologyMermaid(nodesMap, order)
	default:
		return c.renderTopologyBlock(nodesMap, order)
	}
}

type topoNode struct {
	id          string
	name        string
	paused      bool
	downstream  []string
	inputTypes  []string
	outputTypes []string
}

func (c *client) renderTopologyBlock(nodes map[string]topoNode, order []string) int {
	sort.Strings(order)

	fmt.Fprintln(c.stdout, "Station Topology")
	fmt.Fprintln(c.stdout, "===============")
	fmt.Fprint(c.stdout, "\n")

	for i, sid := range order {
		n := nodes[sid]
		if n.id == "" {
			continue
		}
		boxWidth := len(n.name) + 2
		if boxWidth < 12 {
			boxWidth = 12
		}
		padded := fmt.Sprintf(" %s ", n.name)
		for len(padded) < boxWidth {
			padded += " "
		}
		line := strings.Repeat("─", boxWidth-2)
		fmt.Fprintf(c.stdout, "┌─%s─┐\n", line)
		fmt.Fprintf(c.stdout, "│%s│\n", padded)
		fmt.Fprintf(c.stdout, "└─%s─┘\n", line)
		for _, ch := range n.downstream {
			if _, ok := nodes[ch]; ok {
				fmt.Fprintf(c.stdout, "  └──► %s\n", ch)
			}
		}
		if i < len(order)-1 {
			fmt.Fprintln(c.stdout)
		}
	}
	return ExitOK
}

func (c *client) renderTopologyMermaid(nodes map[string]topoNode, order []string) int {
	fmt.Fprintln(c.stdout, "````mermaid")
	fmt.Fprintln(c.stdout, "flowchart LR")

	hasIncoming := make(map[string]bool)
	for _, n := range nodes {
		for _, ch := range n.downstream {
			hasIncoming[ch] = true
		}
	}

	var graphNodes []string
	for _, sid := range order {
		n := nodes[sid]
		if n.id == "" {
			continue
		}
		if len(n.downstream) > 0 || hasIncoming[n.id] {
			graphNodes = append(graphNodes, sid)
		}
	}

	for _, sid := range graphNodes {
		n := nodes[sid]
		nodeLabel := n.id
		if n.name != "" {
			nodeLabel = n.id + "\\n" + strings.ReplaceAll(n.name, "\n", "\\n")
		}
		fmt.Fprintf(c.stdout, "    %s[\"%s\"]\n", sid, nodeLabel)
	}

	for _, sid := range graphNodes {
		n := nodes[sid]
		for _, chID := range n.downstream {
			if chN, ok := nodes[chID]; ok {
				if chN.paused {
					fmt.Fprintf(c.stdout, "    %s -.-> %s [label=downstream (paused)]\n", n.id, chID)
				} else {
					fmt.Fprintf(c.stdout, "    %s --> %s\n", n.id, chID)
				}
			}
		}
	}
	fmt.Fprintln(c.stdout, "````")
	return ExitOK
}

type dataFlowEdge struct {
	from    string
	to      string
	product string
}

func buildDataFlowEdges(nodes map[string]topoNode) []dataFlowEdge {
	var edges []dataFlowEdge
	for _, n := range nodes {
		for _, outType := range n.outputTypes {
			for _, other := range nodes {
				if other.id == n.id {
					continue
				}
				for _, inType := range other.inputTypes {
					if outType == inType {
						edges = append(edges, dataFlowEdge{from: n.id, to: other.id, product: outType})
					}
				}
			}
		}
	}
	return edges
}

func (c *client) renderTopologyInputBlock(nodes map[string]topoNode, order []string) int {
	sort.Strings(order)
	edges := buildDataFlowEdges(nodes)

	products := make(map[string]bool)
	for _, e := range edges {
		products[e.product] = true
	}
	var productOrder []string
	for p := range products {
		productOrder = append(productOrder, p)
	}
	sort.Strings(productOrder)

	fmt.Fprintln(c.stdout, "Station Topology (data flow)")
	fmt.Fprintln(c.stdout, "==========================")
	fmt.Fprint(c.stdout, "\n")

	if len(productOrder) == 0 {
		fmt.Fprintln(c.stdout, "note: no station outputs match any other station's inputs")
		fmt.Fprintln(c.stdout)
		for _, sid := range order {
			n := nodes[sid]
			if n.id == "" {
				continue
			}
			fmt.Fprintf(c.stdout, "  %s (%s):\n", n.id, n.name)
			if len(n.inputTypes) == 0 {
				fmt.Fprintln(c.stdout, "    inputs:  (none)")
			} else {
				fmt.Fprintf(c.stdout, "    inputs:  %s\n", strings.Join(n.inputTypes, ", "))
			}
			if len(n.outputTypes) == 0 {
				fmt.Fprintln(c.stdout, "    outputs: (none)")
			} else {
				fmt.Fprintf(c.stdout, "    outputs: %s\n", strings.Join(n.outputTypes, ", "))
			}
			fmt.Fprintln(c.stdout)
		}
		return ExitOK
	}

	for idx, prod := range productOrder {
		fmt.Fprintf(c.stdout, "product: %s\n", prod)
		fmt.Fprint(c.stdout, "\n  ")
		fmt.Fprint(c.stdout, strings.Repeat("─", 40))
		fmt.Fprint(c.stdout, "\n\n")
		for _, e := range edges {
			if e.product != prod {
				continue
			}
			fmt.Fprintf(c.stdout, "    %s ───[%s]───► %s\n", e.from, prod, e.to)
		}
		fmt.Fprint(c.stdout, "\n")
		if idx < len(productOrder)-1 {
			fmt.Fprintln(c.stdout)
		}
	}
	return ExitOK
}

func (c *client) renderTopologyInputMermaid(nodes map[string]topoNode, order []string) int {
	edges := buildDataFlowEdges(nodes)

	participating := make(map[string]bool)
	for _, e := range edges {
		participating[e.from] = true
		participating[e.to] = true
	}
	var graphOrder []string
	for _, sid := range order {
		if participating[sid] {
			graphOrder = append(graphOrder, sid)
		}
	}

	if len(graphOrder) == 0 && len(edges) == 0 {
		fmt.Fprintln(c.stdout, "````mermaid")
		fmt.Fprintln(c.stdout, "flowchart LR")
		for _, sid := range order {
			n := nodes[sid]
			if n.id == "" {
				continue
			}
			label := n.id
			if n.name != "" {
				label = n.name + " (no matching edges)"
			} else {
				label = n.id + " (no matching edges)"
			}
			fmt.Fprintf(c.stdout, "    %s[\"%s\\ninputs: %s\\noutputs: %s\"]\n",
				sid, label,
				strings.Join(n.inputTypes, ", "),
				strings.Join(n.outputTypes, ", "))
		}
		fmt.Fprintln(c.stdout, "````")
		return ExitOK
	}

	fmt.Fprintln(c.stdout, "````mermaid")
	fmt.Fprintln(c.stdout, "flowchart LR")
	for _, sid := range graphOrder {
		n := nodes[sid]
		if n.name != "" {
			fmt.Fprintf(c.stdout, "    %s[\"%s\\n%s\"]\n", sid, sid, strings.ReplaceAll(n.name, "\n", "\\n"))
		} else {
			fmt.Fprintf(c.stdout, "    %s[\"%s\"]\n", sid, sid)
		}
	}

	type prodEdge struct {
		from string
		to   string
		prod string
	}
	var products []string
	prodEdges := make(map[string][]prodEdge)
	for _, e := range edges {
		if _, ok := prodEdges[e.product]; !ok {
			products = append(products, e.product)
		}
		prodEdges[e.product] = append(prodEdges[e.product], prodEdge{e.from, e.to, e.product})
	}
	sort.Strings(products)

	for _, prod := range products {
		for _, pe := range prodEdges[prod] {
			fmt.Fprintf(c.stdout, "    %s -- %s --> %s\n", pe.from, pe.prod, pe.to)
		}
	}
	fmt.Fprintln(c.stdout, "````")
	return ExitOK
}

func (c *client) renderStationSummaryTable(items []map[string]any) {
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATION ID\tPAUSED\tRUNNING\tQUEUED\tSUCCESS\tFAILURE\tLAST REFRESH")
	for _, it := range items {
		success, failure := "-", "-"
		if counts, ok := it["counts"].(map[string]any); ok {
			success = formatCell(counts["success"])
			failure = formatCell(counts["failure"])
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			formatCell(it["station_id"]),
			formatCell(it["paused"]),
			formatCell(it["running_count"]),
			formatCell(it["queued_count"]),
			success,
			failure,
			formatCell(it["last_refresh"]),
		)
	}
	tw.Flush()
}

// --- subcommand: health / readiness / version -------------------------------

func (c *client) cmdHealth() int {
	m, raw, err := c.do(http.MethodGet, "/health", nil)
	if err != nil {
		return c.reportErr(err)
	}
	c.renderResource(raw, m, []string{"status", "version"})
	return ExitOK
}

func (c *client) cmdReadiness() int {
	m, raw, err := c.do(http.MethodGet, "/readiness", nil)
	if err != nil {
		return c.reportErr(err)
	}
	c.renderResource(raw, m, []string{"status", "ready"})
	return ExitOK
}

func (c *client) cmdVersion(args []string) int {
	checkAPI := false
	for _, a := range args {
		if a == "--check-api" {
			checkAPI = true
		}
	}
	fmt.Fprintf(c.stdout, "veriproc-cli %s (%s)\n", version.Version, version.Commit)
	if !checkAPI {
		return ExitOK
	}
	if _, _, err := c.do(http.MethodGet, "/api/v1/health", nil); err != nil {
		fmt.Fprintln(c.stderr, "api compatibility: "+err.Error())
		return ExitUnavailable
	}
	fmt.Fprintln(c.stdout, "api compatibility: ok")
	return ExitOK
}

// --- helpers ----------------------------------------------------------------

// buildQuery converts --flag args to a URL query string. aliases maps a
// user-facing flag key (post normalisation) to the API query parameter name
// when they differ (e.g. "task" → "task_id"). Unknown flags are silently ignored.
func buildQuery(args, allowed []string, aliases ...map[string]string) string {
	allow := map[string]bool{}
	for _, k := range allowed {
		allow[k] = true
	}
	var aliasMap map[string]string
	if len(aliases) > 0 {
		aliasMap = aliases[0]
	}
	parts := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		key := strings.TrimPrefix(a, "--")
		var val string
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
		} else {
			i++
			if i >= len(args) {
				break
			}
			val = args[i]
		}
		key = strings.ReplaceAll(key, "-", "_")
		if aliasMap != nil {
			if mapped, ok := aliasMap[key]; ok {
				key = mapped
			}
		}
		if !allow[key] {
			continue
		}
		parts = append(parts, key+"="+val)
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `usage: veriproc [--api-url URL] [--token T] [--output table|json|yaml] CMD [ARGS]

Commands:
  submit        --station ID --start TIMESTAMP --end TIMESTAMP [--idempotency-key K] [--force] [--split-group GID]
  task get      TASK_ID
  task list     [--station ID] [--state S] [--split-group GID]
  task retry    TASK_ID
  task delete   [--dry-run] [--force] [--quiet] TASK_ID [TASK_ID...]
  run  get      TASK_ID/rN
  run  list     [--task TASK_ID] [--station STATION_ID] [--state S]
  run  jobs     TASK_ID/rN
  run  delete   [--dry-run] [--force] [--quiet] TASK_ID/rN [TASK_ID/rN...]
  artifact list --run TASK_ID/rN [--type LOGICAL]
  logs          TASK_ID/rN
  cancel        --yes [--reason TEXT] TASK_ID/rN
  promote       --reason R --actor A TASK_ID/rN
  group list    [--state open|aggregating|complete|failed]
  group get     GROUP_ID
  group close   GROUP_ID
  station list
  station summary [STATION_ID] [--since RFC3339]
  station pause      STATION_ID
  station unpause    STATION_ID
  station topology   [--format block|mermaid] [--by-input]
  clean         (--before TS | --after TS) [--by BASIS] [--dry-run] [--force] [--quiet]
  health
  readiness
  version       [--check-api]

Delete/clean flags:
  --dry-run   show what would be deleted, then exit without deleting
  --force     skip the interactive confirmation prompt
  --quiet     suppress the pre-delete content listing (use with --force for
              fully non-interactive scripted deletion)

Environment: VERIPROC_API_URL, VERIPROC_TOKEN, VERIPROC_OUTPUT, VERIPROC_TIMEOUT.
Exit codes follow Spec §6.10 (0 ok, 2 usage, 3 validation, 4 not_found,
5 conflict, 6 auth, 7 unavailable, 8 indeterminate).`)
}
