package console

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UpstreamClient talks to a single veriprocd instance through the documented
// REST surface (Spec §8.9.1).
type UpstreamClient interface {
	Health(ctx context.Context) error
	// GetHealth fetches the upstream health report (status, version, api_version, instance_id).
	GetHealth(ctx context.Context) (map[string]any, error)
	Stations(ctx context.Context) ([]StationListItem, error)
	StationsSummary(ctx context.Context, since time.Time) (StationsSummary, error)
	StationSummary(ctx context.Context, stationID string, since time.Time) (StationSummary, error)
	PauseStation(ctx context.Context, stationID string) (map[string]any, error)
	UnpauseStation(ctx context.Context, stationID string) (map[string]any, error)
	SubmitTask(ctx context.Context, body map[string]any) (map[string]any, error)
	GetTask(ctx context.Context, taskID string) (map[string]any, error)
	RetryTask(ctx context.Context, taskID string, body map[string]any) (map[string]any, error)
	// ListTasks fetches tasks matching the supplied filter query params
	// (station_id, state, limit). Returns the items array.
	ListTasks(ctx context.Context, query url.Values) ([]map[string]any, error)
	ListTaskRuns(ctx context.Context, taskID string) ([]map[string]any, error)
	GetRun(ctx context.Context, runID string) (map[string]any, error)
	ListRunJobs(ctx context.Context, runID string) (map[string]any, error)
	ListRunArtifacts(ctx context.Context, runID string) (map[string]any, error)
	ListRunLogs(ctx context.Context, runID string) (map[string]any, error)
	CancelRun(ctx context.Context, runID string, body map[string]any) (map[string]any, error)
}

// HTTPUpstreamClient implements UpstreamClient against a live veriprocd REST
// API. It is safe for concurrent use.
type HTTPUpstreamClient struct {
	baseURL  string
	token    string
	hc       *http.Client
	slotCap  int
}

// NewHTTPUpstreamClient constructs an UpstreamClient for the supplied
// instance configuration.
func NewHTTPUpstreamClient(inst InstanceConfig) (*HTTPUpstreamClient, error) {
	tok, err := ResolveInstanceToken(inst)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{}
	if inst.InsecureSkipTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	timeout := time.Duration(inst.UpstreamTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPUpstreamClient{
		baseURL: strings.TrimRight(inst.BaseURL, "/"),
		token:   tok,
		hc:      &http.Client{Timeout: timeout, Transport: tr},
	}, nil
}

// UpstreamError carries the upstream HTTP status and decoded error body.
type UpstreamError struct {
	Status int
	Body   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.Status, e.Body)
}

// IsUpstreamStatus reports whether err is an UpstreamError with the given
// HTTP status code.
func IsUpstreamStatus(err error, code int) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Status == code
	}
	return false
}

func (c *HTTPUpstreamClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("upstream: marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return fmt.Errorf("upstream: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("upstream: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	bs, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return &UpstreamError{Status: resp.StatusCode, Body: string(bs)}
	}
	if out == nil || len(bs) == 0 {
		return nil
	}
	if err := json.Unmarshal(bs, out); err != nil {
		return fmt.Errorf("upstream: decode %s %s: %w", method, path, err)
	}
	return nil
}

// Health performs a lightweight readiness check.
func (c *HTTPUpstreamClient) Health(ctx context.Context) error {
	return c.do(ctx, "GET", "/api/v1/health", nil, nil, nil)
}

func (c *HTTPUpstreamClient) GetHealth(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/health", nil, nil, &out)
	return out, err
}

// StationListItem mirrors the shape of /api/v1/stations items.
type StationListItem struct {
	StationID   string `json:"station_id"`
	StationName string `json:"station_name,omitempty"`
	Paused      bool   `json:"paused"`
}

type stationsListResponse struct {
	Items []StationListItem `json:"items"`
}

func (c *HTTPUpstreamClient) Stations(ctx context.Context) ([]StationListItem, error) {
	var resp stationsListResponse
	if err := c.do(ctx, "GET", "/api/v1/stations", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// SummarySlot is one occupant in an upstream station summary.
type SummarySlot struct {
	Kind        string    `json:"kind"`
	RunID       string    `json:"run_id,omitempty"`
	TaskID      string    `json:"task_id"`
	RetryIndex  int       `json:"retry_index"`
	State       string    `json:"state"`
	TerminalAt  *time.Time `json:"terminal_at,omitempty"`
}

// SummaryCounts is the success/failure pair returned by the upstream summary.
type SummaryCounts struct {
	Success int `json:"success"`
	Failure int `json:"failure"`
}

// StationSummary mirrors one item from /api/v1/stations/summary.
type StationSummary struct {
	StationID    string         `json:"station_id"`
	StationName  string         `json:"station_name,omitempty"`
	Paused       bool           `json:"paused"`
	RunningCount int            `json:"running_count"`
	QueuedCount  int            `json:"queued_count"`
	Counts       SummaryCounts  `json:"counts"`
	Slots        []SummarySlot  `json:"slots"`
	LastRefresh  *time.Time     `json:"last_refresh,omitempty"`
	Downstream   []string       `json:"downstream,omitempty"`
}

// StationsSummary is the multi-station summary response.
type StationsSummary struct {
	Since time.Time        `json:"since"`
	Items []StationSummary `json:"items"`
}

func (c *HTTPUpstreamClient) StationsSummary(ctx context.Context, since time.Time) (StationsSummary, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	if c.slotCap > 0 {
		q.Set("slot_count", strconv.Itoa(c.slotCap))
	}
	var out StationsSummary
	if err := c.do(ctx, "GET", "/api/v1/stations/summary", q, nil, &out); err != nil {
		return StationsSummary{}, err
	}
	return out, nil
}

type stationOneSummaryResponse struct {
	Since   time.Time      `json:"since"`
	Station StationSummary `json:"station"`
}

func (c *HTTPUpstreamClient) StationSummary(ctx context.Context, stationID string, since time.Time) (StationSummary, error) {
	q := url.Values{}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	if c.slotCap > 0 {
		q.Set("slot_count", strconv.Itoa(c.slotCap))
	}
	var out stationOneSummaryResponse
	if err := c.do(ctx, "GET", "/api/v1/stations/"+url.PathEscape(stationID)+"/summary", q, nil, &out); err != nil {
		return StationSummary{}, err
	}
	return out.Station, nil
}

func (c *HTTPUpstreamClient) PauseStation(ctx context.Context, stationID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "POST", "/api/v1/stations/"+url.PathEscape(stationID)+"/pause", nil, map[string]any{}, &out)
	return out, err
}

func (c *HTTPUpstreamClient) UnpauseStation(ctx context.Context, stationID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "POST", "/api/v1/stations/"+url.PathEscape(stationID)+"/unpause", nil, map[string]any{}, &out)
	return out, err
}

func (c *HTTPUpstreamClient) SubmitTask(ctx context.Context, body map[string]any) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "POST", "/api/v1/tasks", nil, body, &out)
	return out, err
}

func (c *HTTPUpstreamClient) GetTask(ctx context.Context, taskID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(taskID), nil, nil, &out)
	return out, err
}

type taskListResponse struct {
	Items []map[string]any `json:"items"`
}

func (c *HTTPUpstreamClient) ListTasks(ctx context.Context, query url.Values) ([]map[string]any, error) {
	var resp taskListResponse
	if err := c.do(ctx, "GET", "/api/v1/tasks", query, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *HTTPUpstreamClient) RetryTask(ctx context.Context, taskID string, body map[string]any) (map[string]any, error) {
	var out map[string]any
	if body == nil {
		body = map[string]any{}
	}
	err := c.do(ctx, "POST", "/api/v1/tasks/"+url.PathEscape(taskID)+"/retry", nil, body, &out)
	return out, err
}

type taskRunsResponse struct {
	Items []map[string]any `json:"items"`
}

func (c *HTTPUpstreamClient) ListTaskRuns(ctx context.Context, taskID string) ([]map[string]any, error) {
	var raw json.RawMessage
	if err := c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(taskID)+"/runs", nil, nil, &raw); err != nil {
		return nil, err
	}
	// Some upstream code paths wrap in {"items":...} and some return a bare
	// array; tolerate both shapes.
	var wrapped taskRunsResponse
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Items != nil {
		return wrapped.Items, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("upstream: unrecognized task-runs payload: %s", string(raw))
}

func (c *HTTPUpstreamClient) GetRun(ctx context.Context, runID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID), nil, nil, &out)
	return out, err
}

func (c *HTTPUpstreamClient) ListRunJobs(ctx context.Context, runID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+"/jobs", nil, nil, &out)
	return out, err
}

func (c *HTTPUpstreamClient) ListRunArtifacts(ctx context.Context, runID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+"/artifacts", nil, nil, &out)
	return out, err
}

func (c *HTTPUpstreamClient) ListRunLogs(ctx context.Context, runID string) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, "GET", "/api/v1/runs/"+url.PathEscape(runID)+"/logs", nil, nil, &out)
	return out, err
}

func (c *HTTPUpstreamClient) CancelRun(ctx context.Context, runID string, body map[string]any) (map[string]any, error) {
	if body == nil {
		body = map[string]any{}
	}
	var out map[string]any
	err := c.do(ctx, "POST", "/api/v1/runs/"+url.PathEscape(runID)+"/cancel", nil, body, &out)
	return out, err
}

// ResolveRunByTaskAndRetry returns the upstream run matching (task_id,
// retry_index). The console uses the stable (task, retry) identity but
// upstream cancellation operates on the internal run_id.
func ResolveRunByTaskAndRetry(ctx context.Context, c UpstreamClient, taskID string, retryIndex int) (map[string]any, error) {
	runs, err := c.ListTaskRuns(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		ri, ok := runRetryIndex(run)
		if ok && ri == retryIndex {
			return run, nil
		}
	}
	return nil, fmt.Errorf("upstream: no run with task_id=%s retry_index=%d", taskID, retryIndex)
}

func runRetryIndex(run map[string]any) (int, bool) {
	switch v := run["retry_index"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(v)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// RunIDOf extracts run_id from a run map, defaulting to the empty string.
func RunIDOf(run map[string]any) string {
	if s, ok := run["run_id"].(string); ok {
		return s
	}
	return ""
}
