// Package image shares explicit image-reference and build-command mechanics.
// Archive import and registry delivery remain guest-adapter responsibilities.
package image

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/command"
)

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

type Reference struct {
	Repository string
	Tag        string
	Digest     string
}

// Parse requires an explicit tag or SHA-256 digest. Registry ports are not tags.
// It parses the harness's reference fields, not the complete OCI naming grammar.
func Parse(value string) (Reference, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n") || strings.Contains(value, "://") {
		return Reference{}, fmt.Errorf("invalid image reference")
	}
	name, digest, hasDigest := strings.Cut(value, "@")
	ref := Reference{Repository: name, Digest: digest}
	if hasDigest {
		algorithm, encoded, _ := strings.Cut(digest, ":")
		decoded, err := hex.DecodeString(encoded)
		if algorithm != "sha256" || err != nil || len(decoded) != 32 {
			return Reference{}, fmt.Errorf("image digest must be sha256 with 64 hex digits")
		}
	}
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		ref.Repository, ref.Tag = name[:colon], name[colon+1:]
		if !tagPattern.MatchString(ref.Tag) {
			return Reference{}, fmt.Errorf("invalid image tag")
		}
	}
	if ref.Repository == "" || strings.HasSuffix(ref.Repository, "/") || strings.HasPrefix(ref.Repository, "/") || strings.Contains(ref.Repository, "//") {
		return Reference{}, fmt.Errorf("image repository is required")
	}
	if ref.Tag == "" && ref.Digest == "" {
		return Reference{}, fmt.Errorf("image must carry an explicit tag or digest")
	}
	return ref, nil
}

// SplitTagged supplies the existing ImageReference repository/tag contract.
// A digest is never silently split into a bogus tag.
func SplitTagged(value string) (string, string, error) {
	ref, err := Parse(value)
	if err != nil {
		return "", "", err
	}
	if ref.Digest != "" || ref.Tag == "" {
		return "", "", fmt.Errorf("tag-only consumer cannot accept digest image reference")
	}
	return ref.Repository, ref.Tag, nil
}

// OperandBuild constructs the common Dockerfile build step without executing it.
// The caller supplies the explicit environment needed by its local Docker setup.
func OperandBuild(root, dockerfile, reference string, timeout time.Duration, env map[string]string) (command.Spec, error) {
	if _, _, err := SplitTagged(reference); err != nil {
		return command.Spec{}, err
	}
	if !filepath.IsAbs(root) || filepath.IsAbs(dockerfile) || timeout <= 0 {
		return command.Spec{}, fmt.Errorf("absolute build root, relative Dockerfile, and positive timeout required")
	}
	clean := filepath.Clean(dockerfile)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return command.Spec{}, fmt.Errorf("build file must remain under the build root")
	}
	return command.Spec{Name: "docker", Args: []string{"build", "-f", filepath.Join(root, clean), "-t", reference, root},
		Dir: root, Env: env, Timeout: timeout}, nil
}

// ManagerBuild preserves the repository's provenance-aware Makefile target.
func ManagerBuild(root, reference string, timeout time.Duration, env map[string]string) (command.Spec, error) {
	if _, _, err := SplitTagged(reference); err != nil {
		return command.Spec{}, err
	}
	return command.Make(root, timeout, env, "docker-build", "IMG="+reference)
}
