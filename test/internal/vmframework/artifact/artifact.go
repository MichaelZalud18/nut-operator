// Package artifact prepares pinned VM boot artifacts independently of guest provisioning.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Source is either a local file or an HTTP(S) artifact. Remote artifacts require
// SHA256. Local files may omit it for compatibility with locally built images.
type Source struct {
	Location string
	SHA256   string
}

// ParseSHA256 accepts bare hex or the sha256: form used by both VM adapters.
func ParseSHA256(value string) ([]byte, error) {
	algorithm, digest, prefixed := strings.Cut(value, ":")
	if !prefixed {
		digest = value
	} else if !strings.EqualFold(algorithm, "sha256") {
		return nil, fmt.Errorf("artifact checksum must use sha256")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("artifact checksum must contain 64 hexadecimal SHA-256 digits")
	}
	return decoded, nil
}

// Prepare returns a verified local path. Remote data is streamed to a private
// temporary file and published at destination only after checksum validation.
// Existing destinations are never overwritten. Failed/cancelled downloads remove
// only their own temporary file, preserving all other diagnostic state.
// A ten-minute ceiling applies unless the caller's deadline expires sooner.
// Local sources remain in place; destination is unused for them.
func Prepare(ctx context.Context, client *http.Client, source Source, destination string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	digest, remote, err := validate(source)
	if err != nil {
		return "", err
	}
	if !remote {
		path, err := filepath.Abs(source.Location)
		if err != nil {
			return "", err
		}
		if err := Verify(ctx, path, digest); err != nil {
			return "", err
		}
		return path, nil
	}
	return download(ctx, client, source.Location, destination, digest)
}

func validate(source Source) ([]byte, bool, error) {
	if source.Location == "" {
		return nil, false, fmt.Errorf("artifact location is required")
	}
	var digest []byte
	if source.SHA256 != "" {
		var err error
		digest, err = ParseSHA256(source.SHA256)
		if err != nil {
			return nil, false, err
		}
	}
	u, err := url.Parse(source.Location)
	if err != nil {
		return nil, false, fmt.Errorf("invalid artifact location")
	}
	remote := u.Scheme != ""
	if remote && ((u.Scheme != "https" && u.Scheme != "http") || u.Host == "") {
		return nil, false, fmt.Errorf("remote artifact must use HTTP or HTTPS")
	}
	if remote && len(digest) == 0 {
		return nil, false, fmt.Errorf("remote artifact requires a pinned SHA-256 checksum")
	}
	return digest, remote, nil
}

func download(ctx context.Context, client *http.Client, location, destination string, digest []byte) (string, error) {
	if destination == "" {
		return "", fmt.Errorf("download destination is required")
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("artifact destination already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".vm-artifact-*")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), contextReader{ctx, resp.Body}); err != nil {
		return "", err
	}
	if !bytes.Equal(hash.Sum(nil), digest) {
		return "", fmt.Errorf("artifact SHA-256 checksum mismatch")
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Linking within one directory atomically publishes the complete file without
	// replacing a destination created concurrently by another caller.
	if err := os.Link(tmp.Name(), destination); err != nil {
		return "", err
	}
	return destination, nil
}

// Verify checks a regular local file; nil digest allows an unpinned local build.
func Verify(ctx context.Context, path string, digest []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(digest) != 0 && len(digest) != sha256.Size {
		return fmt.Errorf("invalid SHA-256 digest length")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact must be a regular file")
	}
	if len(digest) == 0 {
		return nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx, f}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !bytes.Equal(hash.Sum(nil), digest) {
		return fmt.Errorf("artifact SHA-256 checksum mismatch")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
