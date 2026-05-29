package console

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// FileKind classifies a directory entry for UI rendering.
type FileKind string

const (
	KindDirectory FileKind = "directory"
	KindText      FileKind = "text"
	KindLog       FileKind = "log"
	KindBinary    FileKind = "binary"
	KindSymlink   FileKind = "symlink"
	KindUnknown   FileKind = "unknown"
)

// TreeEntry describes one filesystem entry returned by the working-root
// browser.
type TreeEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Kind    FileKind  `json:"kind"`
}

// TreeResponse is the directory-listing payload returned to the frontend.
type TreeResponse struct {
	WorkingRoot string      `json:"working_root"`
	Path        string      `json:"path"`
	Entries     []TreeEntry `json:"entries"`
}

// PreviewResponse is the file-preview payload returned to the frontend.
type PreviewResponse struct {
	WorkingRoot string   `json:"working_root"`
	Path        string   `json:"path"`
	Kind        FileKind `json:"kind"`
	Size        int64    `json:"size"`
	Offset      int64    `json:"offset"`
	Mode        string   `json:"mode,omitempty"`
	Truncated   bool     `json:"truncated"`
	Bytes       int      `json:"bytes_returned"`
	Content     string   `json:"content,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// PreviewOptions controls how PreviewFile reads a file window.
type PreviewOptions struct {
	MaxBytes int64
	Mode     string // "head" (default) or "tail"
}

// Errors returned by the filesystem layer.
var (
	ErrPathEscape  = errors.New("console: path escapes working root")
	ErrSymlinkEsc  = errors.New("console: symlink escapes working root")
	ErrNotAllowed  = errors.New("console: working root outside allowed bases")
	ErrNotFoundFS  = errors.New("console: not found")
	ErrIsDirectory = errors.New("console: target is a directory")
	ErrTooLarge    = errors.New("console: file exceeds preview limit")
)

// ValidateWorkingRoot ensures the supplied absolute path lies within one of
// the allowed bases configured for the instance.
func ValidateWorkingRoot(allowedRoots []string, workingRoot string) (string, error) {
	if workingRoot == "" {
		return "", errors.New("console: empty working root")
	}
	abs, err := filepath.Abs(workingRoot)
	if err != nil {
		return "", fmt.Errorf("console: abs working root: %w", err)
	}
	abs = filepath.Clean(abs)
	if len(allowedRoots) == 0 {
		return "", ErrNotAllowed
	}
	for _, base := range allowedRoots {
		if abs == base || strings.HasPrefix(abs, ensureTrailingSep(base)) {
			return abs, nil
		}
	}
	return "", ErrNotAllowed
}

func ensureTrailingSep(p string) string {
	if strings.HasSuffix(p, string(os.PathSeparator)) {
		return p
	}
	return p + string(os.PathSeparator)
}

// resolveInsideRoot joins root and rel, refusing any traversal that would
// escape root either lexically or via symlinks.
func resolveInsideRoot(root, rel string) (string, error) {
	// Reject ".." components on the raw input before any normalization. We
	// intentionally do not let filepath.Clean silently collapse "/.." back
	// to "/" — that pattern is exactly what a traversal attempt looks like
	// and must surface as an error.
	rel = strings.TrimPrefix(rel, "/")
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", ErrPathEscape
		}
	}
	rel = filepath.Clean("/" + rel)[1:]
	joined := filepath.Join(root, rel)
	cleaned := filepath.Clean(joined)
	if cleaned != root && !strings.HasPrefix(cleaned, ensureTrailingSep(root)) {
		return "", ErrPathEscape
	}
	// Symlink resolution: walk EvalSymlinks and make sure the result is still
	// rooted under root. EvalSymlinks fails on non-existent paths, so fall
	// back to the lexical check for missing files (handled by caller).
	if real, err := filepath.EvalSymlinks(cleaned); err == nil {
		real = filepath.Clean(real)
		if real != root && !strings.HasPrefix(real, ensureTrailingSep(root)) {
			return "", ErrSymlinkEsc
		}
	}
	return cleaned, nil
}

// ListTree returns one directory listing scoped inside workingRoot.
//
// workingRoot must already have been validated against the instance's
// allowed bases via ValidateWorkingRoot.
func ListTree(workingRoot, rel string) (*TreeResponse, error) {
	target, err := resolveInsideRoot(workingRoot, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFoundFS
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("console: not a directory: %s", rel)
	}
	dirEntries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	out := &TreeResponse{
		WorkingRoot: workingRoot,
		Path:        rel,
		Entries:     make([]TreeEntry, 0, len(dirEntries)),
	}
	for _, de := range dirEntries {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		childRel := filepath.ToSlash(filepath.Join(rel, de.Name()))
		entry := TreeEntry{
			Name:    de.Name(),
			Path:    childRel,
			IsDir:   de.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
			Kind:    classifyEntry(de, fi),
		}
		out.Entries = append(out.Entries, entry)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].IsDir != out.Entries[j].IsDir {
			return out.Entries[i].IsDir
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	return out, nil
}

func classifyEntry(de os.DirEntry, fi os.FileInfo) FileKind {
	mode := fi.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case de.IsDir():
		return KindDirectory
	}
	switch strings.ToLower(filepath.Ext(de.Name())) {
	case ".log":
		return KindLog
	case ".txt", ".md", ".json", ".yaml", ".yml", ".csv", ".xml", ".sh", ".py", ".go", ".out", ".err":
		return KindText
	}
	return KindUnknown
}

// PreviewFile returns an inline preview of target file, or an error when the
// file cannot be safely rendered. maxBytes caps the inline size.
func PreviewFile(workingRoot, rel string, maxBytes int64) (*PreviewResponse, error) {
	return PreviewFileWithOptions(workingRoot, rel, PreviewOptions{MaxBytes: maxBytes})
}

// PreviewFileWithOptions returns an inline preview of target file, or an error
// when the file cannot be safely rendered. It reads only the requested bounded
// window, so large logs can be inspected without loading from the beginning.
func PreviewFileWithOptions(workingRoot, rel string, opts PreviewOptions) (*PreviewResponse, error) {
	target, err := resolveInsideRoot(workingRoot, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFoundFS
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrIsDirectory
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	limit := opts.MaxBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	mode := strings.TrimSpace(strings.ToLower(opts.Mode))
	if mode == "" {
		mode = "head"
	}
	if mode != "tail" {
		mode = "head"
	}
	offset := int64(0)
	readLimit := limit + 1
	if mode == "tail" {
		readLimit = limit
		if info.Size() > limit {
			offset = info.Size() - limit
		}
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, readLimit)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	truncated := offset > 0 || int64(n) > limit
	if truncated {
		n = int(limit)
	}
	data := buf[:n]
	if offset > 0 {
		data, offset = trimLeadingPartialUTF8(data, offset)
	}
	bytesReturned := len(data)
	kind := KindUnknown
	if isText(data) {
		kind = KindText
		if strings.EqualFold(filepath.Ext(target), ".log") {
			kind = KindLog
		}
		return &PreviewResponse{
			WorkingRoot: workingRoot,
			Path:        rel,
			Kind:        kind,
			Size:        info.Size(),
			Offset:      offset,
			Mode:        mode,
			Truncated:   truncated,
			Bytes:       bytesReturned,
			Content:     string(data),
		}, nil
	}
	return &PreviewResponse{
		WorkingRoot: workingRoot,
		Path:        rel,
		Kind:        KindBinary,
		Size:        info.Size(),
		Offset:      offset,
		Mode:        mode,
		Truncated:   truncated,
		Bytes:       bytesReturned,
		Reason:      "binary content; preview not rendered",
	}, nil
}

func trimLeadingPartialUTF8(data []byte, offset int64) ([]byte, int64) {
	if utf8.Valid(data) {
		return data, offset
	}
	for i := 1; i < len(data) && i <= utf8.UTFMax; i++ {
		if utf8.Valid(data[i:]) {
			return data[i:], offset + int64(i)
		}
	}
	return data, offset
}

// isText returns true when buf appears to be valid UTF-8 text without binary
// control characters. Heuristic: reject NUL bytes and require valid utf8.
func isText(buf []byte) bool {
	if len(buf) == 0 {
		return true
	}
	for _, b := range buf {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(buf)
}
