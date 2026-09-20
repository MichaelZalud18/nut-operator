// Package workspace prepares private editable copies instead of modifying and
// later restoring files in a shared checkout.
package workspace

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Clone copies an explicitly selected source subtree into a new private directory
// under parent. Symlinks and special files are rejected; the source is never edited.
// The caller owns the returned directory and decides whether to retain diagnostics
// or delete it after all resources using it have stopped. Failed clones remove only
// their newly allocated directory. Local filesystem calls are not forcibly cancellable.
func Clone(ctx context.Context, source, parent string) (path string, retErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(source, parent)
	if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("workspace destination must be outside its source subtree")
	}
	path, err = os.MkdirTemp(parent, "vm-workspace-")
	if err != nil {
		return "", err
	}
	owned := path
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(owned)
		}
	}()
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	err = filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return root.MkdirAll(rel, 0700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("workspace source contains a symlink or special file")
		}
		return copyFile(ctx, root, name, rel)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func copyFile(ctx context.Context, root *os.Root, source, name string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("workspace source is not a regular file")
	}
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|info.Mode().Perm()&0100)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, contextReader{ctx, in}); err != nil {
		return err
	}
	return out.Close()
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}
