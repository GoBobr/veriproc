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
//	group close     POST   /api/v1/groups/{id}/close
//	health          GET    /health
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

const cliVersion = "0.8.0-m8"

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
	case "task", "run", "artifact", "group", "groups":
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
	c.renderResource(raw, map[string]any{"task": task, "links": m["links"]},
		[]string{"task_id", "station_id", "start", "end", "state", "latest_retry_index", "latest_run_ref", "canonical_retry_index", "canonical_run_ref", "split_group_id"})
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
		c.renderResource(raw, m, []string{"task_id", "station_id", "start", "end", "state", "latest_retry_index", "latest_run_ref", "canonical_retry_index", "canonical_run_ref", "split_group_id", "created_at"})
		return ExitOK
	case "list":
		q := buildQuery(args, []string{"station_id", "state", "split_group_id", "parent_task_id", "limit", "cursor"})
		m, raw, err := c.do(http.MethodGet, "/api/v1/tasks"+q, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"task_id", "station_id", "start", "end", "state", "latest_retry_index", "latest_run_ref", "canonical_retry_index", "canonical_run_ref", "created_at"})
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
	default:
		fmt.Fprintln(c.stderr, "veriproc task {get|list|retry}")
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
		c.renderResource(raw, m, []string{"run_ref", "task_id", "retry_index", "station_id", "start", "end", "state", "canonicality", "created_at", "working_root"})
		return ExitOK
	case "list":
		q := buildQuery(args, []string{"task_id", "state", "canonicality", "station_id", "limit", "cursor"})
		m, raw, err := c.do(http.MethodGet, "/api/v1/runs"+q, nil)
		if err != nil {
			return c.reportErr(err)
		}
		c.renderList(raw, m, []string{"run_ref", "task_id", "retry_index", "state", "canonicality", "working_root", "created_at"})
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
		c.renderList(raw, m, []string{"job_id", "executor_type", "scheduler_native_state", "submitted_at"})
		return ExitOK
	default:
		fmt.Fprintln(c.stderr, "veriproc run {get|list|jobs}")
		return ExitUsage
	}
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
		c.renderList(raw, m, []string{"artifact_id", "logical_type", "availability", "size", "checksum_source", "created_at"})
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
	c.renderList(raw, m, []string{"artifact_id", "logical_type", "availability", "created_at"})
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
	fmt.Fprintf(c.stdout, "veriproc-cli %s (api v1)\n", cliVersion)
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

// buildQuery converts ["--state", "open"] into "?state=open" for the listed
// keys; unknown flags are silently ignored.
func buildQuery(args, allowed []string) string {
	allow := map[string]bool{}
	for _, k := range allowed {
		allow[k] = true
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
  submit        --station ID --start RFC3339 --end RFC3339 [--idempotency-key K] [--force] [--split-group GID]
  task get      TASK_ID
  task list     [--station ID] [--state S] [--split-group GID]
  task retry    TASK_ID
  run  get      TASK_ID/rN
  run  list     [--task TASK_ID] [--state S]
  run  jobs     TASK_ID/rN
  artifact list --run TASK_ID/rN [--type LOGICAL]
  logs          TASK_ID/rN
  cancel        --yes [--reason TEXT] TASK_ID/rN
  promote       --reason R --actor A TASK_ID/rN
  group list    [--state open|aggregating|complete|failed]
  group get     GROUP_ID
  group close   GROUP_ID
  health
  readiness
  version       [--check-api]

Environment: VERIPROC_API_URL, VERIPROC_TOKEN, VERIPROC_OUTPUT, VERIPROC_TIMEOUT.
Exit codes follow Spec §6.10 (0 ok, 2 usage, 3 validation, 4 not_found,
5 conflict, 6 auth, 7 unavailable, 8 indeterminate).`)
}
