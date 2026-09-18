//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestMetricsProbePodTokenAndSecurity(t *testing.T) {
	p := metricsProbePod()
	if p.Spec.ServiceAccountName != serviceAccountName || p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		t.Fatal("probe must use the intended service account with only its explicit token projection")
	}
	if len(p.Spec.Containers) != 1 || len(p.Spec.Volumes) != 1 || p.Spec.RestartPolicy != corev1.RestartPolicyNever || p.Spec.HostNetwork || p.Spec.HostPID || p.Spec.HostIPC {
		t.Fatal("unexpected pod boundary")
	}
	assertMetricsTokenProjection(t, p.Spec.Volumes[0], p.Spec.Containers[0])
	assertMetricsContainerSecurity(t, p.Spec.Containers[0].SecurityContext)
	assertMetricsRuntimeManifest(t, p)
}

func assertMetricsTokenProjection(t *testing.T, v corev1.Volume, c corev1.Container) {
	t.Helper()
	if v.Projected == nil || len(v.Projected.Sources) != 1 || v.Projected.Sources[0].ServiceAccountToken == nil || v.Secret != nil || v.HostPath != nil {
		t.Fatal("token must be projected by Kubernetes, not embedded or supplied by the host")
	}
	projection := v.Projected.Sources[0].ServiceAccountToken
	if projection.Path != "token" || projection.ExpirationSeconds == nil || *projection.ExpirationSeconds != 3600 || projection.Audience != "" {
		t.Fatal("unexpected projected token configuration")
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].Name != v.Name || c.VolumeMounts[0].MountPath != metricsTokenDirectory || !c.VolumeMounts[0].ReadOnly || len(c.Env) != 0 || len(c.EnvFrom) != 0 {
		t.Fatal("token must come only from the read-only standard mount")
	}
}

func assertMetricsContainerSecurity(t *testing.T, s *corev1.SecurityContext) {
	t.Helper()
	if s == nil || s.ReadOnlyRootFilesystem == nil || !*s.ReadOnlyRootFilesystem || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation || s.RunAsNonRoot == nil || !*s.RunAsNonRoot || s.RunAsUser == nil || *s.RunAsUser != 1000 || s.SeccompProfile == nil || s.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || s.Capabilities == nil || len(s.Capabilities.Add) != 0 || !reflect.DeepEqual(s.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatal("probe security boundary changed")
	}
}

func assertMetricsRuntimeManifest(t *testing.T, p corev1.Pod) {
	t.Helper()
	c := p.Spec.Containers[0]
	if !reflect.DeepEqual(c.Command, []string{"/bin/sh", "-c"}) || len(c.Args) != 6 || c.Args[0] != metricsProbeScript || c.Args[2] != metricsTokenDirectory+"/token" || c.Args[4] != "30" || c.Args[5] != "2" {
		t.Fatal("unexpected runtime token reader or retry bound")
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"metrics-test-secret", "TokenRequest", "--verbose", "curl -v", "set -x"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("manifest contains %q", forbidden)
		}
	}
}

func TestMetricsProbeRuntimeAuthenticationAndHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name, response, code string
		ok                   bool
	}{
		{"valid metric", "# TYPE go_goroutines gauge\ngo_goroutines 12\n200", "0", true},
		{"HTTP forbidden", "go_goroutines 12\n403", "22", false},
		{"HTTP failure even with metric", "go_goroutines 12\n500", "0", false},
		{"redirect", "go_goroutines 12\n302", "0", false},
		{"missing metric", "# go_goroutines 12\n200", "0", false},
		{"invalid metric", "go_goroutines nope\n200", "0", false},
		{"timeout", "", "28", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tokenPath := filepath.Join(dir, "token")
			const secret = "metrics-test-secret"
			if err := os.WriteFile(tokenPath, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			stub := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$METRICS_ARGS"
header=$(cat)
[ "$header" = 'Authorization: Bearer metrics-test-secret' ] || exit 91
printf '%s' "$METRICS_RESPONSE"
exit "$METRICS_EXIT"
`
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			argsPath := filepath.Join(dir, "args")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", metricsProbeScript, "metrics-probe", tokenPath, "https://metrics.example/metrics", "1", "0")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "METRICS_ARGS="+argsPath, "METRICS_RESPONSE="+tc.response, "METRICS_EXIT="+tc.code)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("probe exceeded test deadline")
			}
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v, err=%v, output=%s", tc.ok, err, out)
			}
			if strings.Contains(string(out), secret) || strings.Contains(strings.Join(cmd.Args, " "), secret) {
				t.Fatal("token leaked into output or shell arguments")
			}
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"--disable", "--silent", "--show-error", "--fail", "--insecure", "--max-time", "5", "--header", "@-", "--write-out", "\\n%{http_code}", "https://metrics.example/metrics"}
			if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"), want) {
				t.Fatalf("unexpected curl arguments: %q", args)
			}
			if tc.ok && string(out) != "HTTP 200\ngo_goroutines 12\n" {
				t.Fatalf("unexpected evidence: %q", out)
			}
		})
	}
}
