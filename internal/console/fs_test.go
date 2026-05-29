package console

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWorkingRoot(t *testing.T) {
	base := t.TempDir()
	good := filepath.Join(base, "run1")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateWorkingRoot([]string{base}, good)
	if err != nil {
		t.Errorf("good path rejected: %v", err)
	}
	_, err = ValidateWorkingRoot([]string{base}, "/etc")
	if err == nil {
		t.Error("expected /etc to be rejected")
	}
}

func TestListTree_RejectsTraversal(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "run")
	if err := os.MkdirAll(filepath.Join(run, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "logs", "app.log"), []byte("hello\nworld"), 0o644); err != nil {
		t.Fatal(err)
	}
	// listing the run root itself
	tree, err := ListTree(run, "")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != "logs" {
		t.Fatalf("unexpected entries: %+v", tree.Entries)
	}
	// traversal
	if _, err := ListTree(run, "../"); err == nil {
		t.Error("expected ../ traversal to fail")
	}
	if _, err := ListTree(run, "logs/../../"); err == nil {
		t.Error("expected nested traversal to fail")
	}
}

func TestListTree_SymlinkEscapeBlocked(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "run")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink inside the run points outside.
	if err := os.Symlink(outside, filepath.Join(run, "escape")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ListTree(run, "escape"); err == nil {
		t.Error("expected symlink escape to be rejected")
	}
}

func TestPreviewFile_TextAndBinary(t *testing.T) {
	run := t.TempDir()
	textPath := filepath.Join(run, "a.txt")
	if err := os.WriteFile(textPath, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := PreviewFile(run, "a.txt", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindText {
		t.Errorf("kind = %q, want text", out.Kind)
	}
	if !strings.HasPrefix(out.Content, "hello") {
		t.Errorf("content = %q", out.Content)
	}
	if out.Truncated {
		t.Error("small file should not be truncated")
	}

	// binary
	binPath := filepath.Join(run, "b.bin")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = PreviewFile(run, "b.bin", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindBinary {
		t.Errorf("kind = %q, want binary", out.Kind)
	}
	if out.Content != "" {
		t.Errorf("binary content must not be rendered inline")
	}
}

func TestPreviewFile_TruncationAtLimit(t *testing.T) {
	run := t.TempDir()
	path := filepath.Join(run, "big.log")
	body := strings.Repeat("a", 2048)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := PreviewFile(run, "big.log", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Error("file exceeding limit should be truncated")
	}
	if int64(out.Bytes) != 1024 {
		t.Errorf("bytes = %d, want 1024", out.Bytes)
	}
	if out.Kind != KindLog {
		t.Errorf("kind = %q, want log", out.Kind)
	}
}

func TestPreviewFile_TailModeReadsEndOnly(t *testing.T) {
	run := t.TempDir()
	path := filepath.Join(run, "big.log")
	body := "first line\nsecond line\nlast line\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := PreviewFileWithOptions(run, "big.log", PreviewOptions{MaxBytes: 10, Mode: "tail"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "tail" {
		t.Fatalf("mode = %q, want tail", out.Mode)
	}
	if !out.Truncated || out.Offset == 0 {
		t.Fatalf("tail preview should report truncation and offset: %+v", out)
	}
	if !strings.Contains(out.Content, "last line") {
		t.Fatalf("tail content = %q, want final line", out.Content)
	}
}

func TestPreviewFile_NotFound(t *testing.T) {
	run := t.TempDir()
	_, err := PreviewFile(run, "missing", 1024)
	if err != ErrNotFoundFS {
		t.Errorf("err = %v, want ErrNotFoundFS", err)
	}
}

func TestHideRun_Idempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := HiddenRunKey{InstanceID: "vp1", StationID: "STA", TaskID: "t1", RetryIndex: 0}
	if err := db.HideRun(ctx, key, "alice", nowForTest()); err != nil {
		t.Fatal(err)
	}
	// Repeat. Must not error.
	if err := db.HideRun(ctx, key, "bob", nowForTest()); err != nil {
		t.Fatal(err)
	}
	hidden, err := db.HiddenForStation(ctx, "vp1", "STA")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hidden[key]; !ok {
		t.Fatal("hidden record missing")
	}
	if len(hidden) != 1 {
		t.Errorf("expected exactly one hidden row, got %d", len(hidden))
	}
}
