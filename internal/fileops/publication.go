package fileops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	ObjectKindRegularFile = "regular_file"
	ObjectKindDirectory   = "directory"
)

func ObjectKind(info os.FileInfo) string {
	if info != nil && info.IsDir() {
		return ObjectKindDirectory
	}
	return ObjectKindRegularFile
}

func PublishObject(src, dst, mode string) error {
	if mode == "" {
		mode = "copy"
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat target: %w", err)
	}

	switch mode {
	case "copy":
		if info.IsDir() {
			return copyDirAtomic(src, dst)
		}
		return copyFileAtomic(src, dst, info.Mode().Perm())
	case "move":
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
		if info.IsDir() {
			if err := copyDirAtomic(src, dst); err != nil {
				return err
			}
			return os.RemoveAll(src)
		}
		if err := copyFileAtomic(src, dst, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Remove(src)
	case "symlink":
		tmp := dst + ".part"
		_ = os.Remove(tmp)
		if err := os.Symlink(src, tmp); err != nil {
			return fmt.Errorf("symlink target: %w", err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename symlink target: %w", err)
		}
		return nil
	case "hardlink":
		if info.IsDir() {
			return fmt.Errorf("hardlink publication is not supported for directories")
		}
		if err := os.Link(src, dst); err != nil {
			return fmt.Errorf("hardlink target: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported publication mode %q", mode)
	}
}

func copyFileAtomic(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	tmp := dst + ".part"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create target: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close target: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename target: %w", err)
	}
	return nil
}

func copyDirAtomic(src, dst string) error {
	tmp := dst + ".part"
	_ = os.RemoveAll(tmp)
	if err := copyDir(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("rename target directory: %w", err)
	}
	return nil
}

func copyDir(src, dst string) error {
	rootInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source directory: %w", err)
	}
	if err := os.MkdirAll(dst, rootInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("mkdir target directory: %w", err)
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == src {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		return copyFileAtomic(path, target, info.Mode().Perm())
	})
}
