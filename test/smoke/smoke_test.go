// Package smoke contains process-level smoke tests for the veriprocd binary.
//
// These tests build the binary on the fly and exercise startup + graceful
// shutdown behavior end-to-end. They are tagged so that `go test -short`
// skips them.
package smoke

import (
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestSmoke_StartShutdown_M0 — build veriprocd, start it on an ephemeral port,
// hit /health, then send SIGTERM and assert clean exit.
func TestSmoke_StartShutdown_M0(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("smoke test relies on POSIX signals")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "veriprocd")

	build := exec.Command("go", "build", "-o", bin, "./cmd/veriprocd")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	addr := pickAddr(t)
	stationRoot := filepath.Join(dir, "stations")
	writeSmokeStation(t, stationRoot)
	cmd := exec.Command(bin,
		"--http-addr", addr,
		"--instance-id", "smoke-test",
		"--log-level", "warn",
	)
	cmd.Env = append(os.Environ(), "VERIPROC_STATION_CONFIG_ROOT="+stationRoot)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	if err := waitForHTTP("http://"+addr+"/health", 5*time.Second); err != nil {
		t.Fatalf("server not healthy: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within shutdown deadline")
	}
}

func writeSmokeStation(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "station-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir station: %v", err)
	}
	content := []byte("station_id: SMOKE-STATION\nproc_type: SMOKE_PROC\nscripts:\n  run: ./scripts/run.sh\n")
	if err := os.WriteFile(filepath.Join(dir, "station.yaml"), content, 0o644); err != nil {
		t.Fatalf("write station: %v", err)
	}
}

func pickAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = errStatus(resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return lastErr
}

type errStatus int

func (e errStatus) Error() string {
	return http.StatusText(int(e))
}

// repoRoot finds the repository root by walking up from the test file until
// it sees a go.mod. This keeps the smoke test independent of CWD.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := exec.Command("pwd").Output()
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	dir := string(wd)
	if n := len(dir); n > 0 && dir[n-1] == '\n' {
		dir = dir[:n-1]
	}
	for {
		if _, err := exec.Command("test", "-f", filepath.Join(dir, "go.mod")).Output(); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root from %s", dir)
		}
		dir = parent
	}
}
