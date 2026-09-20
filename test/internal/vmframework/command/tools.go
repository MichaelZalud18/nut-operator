package command

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Kubectl always selects the caller's explicit kubeconfig on the command line.
// It does not fall back to HOME, KUBECONFIG, or the ambient current context.
func Kubectl(kubeconfig string, timeout time.Duration, input io.Reader, args ...string) (Spec, error) {
	if !filepath.IsAbs(kubeconfig) {
		return Spec{}, fmt.Errorf("kubectl requires an absolute private kubeconfig path")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-s") && !strings.HasPrefix(arg, "--") {
			return Spec{}, fmt.Errorf("kubectl arguments cannot override the selected cluster")
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--kubeconfig", "--context", "--server", "--cluster", "--user", "-s":
			return Spec{}, fmt.Errorf("kubectl arguments cannot override the selected cluster")
		}
	}
	return Spec{Name: "kubectl", Args: append([]string{"--kubeconfig", kubeconfig}, args...), Stdin: input, Timeout: timeout}, nil
}

// Make constructs an explicit repository-scoped command. Environment and targets
// are supplied by the caller so image provenance/build flags remain visible.
func Make(root string, timeout time.Duration, env map[string]string, targets ...string) (Spec, error) {
	if !filepath.IsAbs(root) || timeout <= 0 {
		return Spec{}, fmt.Errorf("make requires an absolute work directory")
	}
	return Spec{Name: "make", Args: targets, Dir: root, Env: env, Timeout: timeout}, nil
}

// RepositoryRoot replaces the duplicated Git lookup without assuming the test's
// working directory is the repository root.
func RepositoryRoot(ctx context.Context, directory string) (string, error) {
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("repository lookup requires an absolute starting directory")
	}
	result, err := Run(ctx, Spec{Name: "git", Args: []string{"rev-parse", "--show-toplevel"}, Dir: directory, Timeout: 10 * time.Second})
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(result.Stdout)
	if !filepath.IsAbs(root) || result.StdoutTruncated {
		return "", fmt.Errorf("git did not return a complete absolute repository root")
	}
	return root, nil
}
