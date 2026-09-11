//go:build hadron
// +build hadron

/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hadron

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// requireISOTool skips the test rather than failing it when neither genisoimage nor mkisofs is
// installed: that is an environment gap (this WSL dev box has neither), not a code defect, and
// make test-hadron / CI installs one of them alongside qemu.
func requireISOTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("genisoimage"); err == nil {
		return
	}
	if _, err := exec.LookPath("mkisofs"); err == nil {
		return
	}
	t.Skip("neither genisoimage nor mkisofs is installed")
}

func TestBuildNoCloudISOProducesANonEmptyImage(t *testing.T) {
	requireISOTool(t)
	dir := t.TempDir()

	path, err := buildNoCloudISO(dir, "#cloud-config\nfoo: bar\n")
	if err != nil {
		t.Fatalf("buildNoCloudISO: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Error("expected a non-empty ISO")
	}
}

func TestBuildNoCloudISORejectsMissingTool(t *testing.T) {
	if _, err := exec.LookPath("genisoimage"); err == nil {
		t.Skip("genisoimage is installed; cannot exercise the missing-tool path")
	}
	if _, err := exec.LookPath("mkisofs"); err == nil {
		t.Skip("mkisofs is installed; cannot exercise the missing-tool path")
	}
	if _, err := buildNoCloudISO(t.TempDir(), "#cloud-config\n"); err == nil {
		t.Fatal("expected an error with no ISO tool on PATH")
	}
}

func TestKairosAutoInstallCloudConfigEmbedsCredentialsAndDevice(t *testing.T) {
	creds := Credentials{User: "hadron-test-user", Pass: "hadron-test-pass"}
	cfg := KairosAutoInstallCloudConfig(creds, "/dev/vda")

	for _, want := range []string{
		"#cloud-config",
		`device: "/dev/vda"`,
		"reboot: true",
		"auto: true",
		"k3s:",
		"enabled: true",
		"name: hadron-test-user",
		"passwd: hadron-test-pass",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cloud-config missing %q:\n%s", want, cfg)
		}
	}
}

func TestNewSafeMachineAttachesCloudConfigAsDataSource(t *testing.T) {
	requireISOTool(t)

	m, creds, err := NewSafeMachine(Config{
		CloudConfig: KairosAutoInstallCloudConfig(Credentials{User: "u", Pass: "p"}, "/dev/vda"),
	})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })

	if creds.User == "" {
		t.Fatal("expected NewSafeMachine to still generate its own SSH credentials independent of CloudConfig")
	}

	ds := m.Config().DataSource
	if ds == "" {
		t.Fatal("expected DataSource to be set when CloudConfig is provided")
	}
	if _, err := os.Stat(ds); err != nil {
		t.Errorf("DataSource %s: %v", ds, err)
	}
}

func TestNewSafeMachineOmitsDataSourceWithoutCloudConfig(t *testing.T) {
	m, _, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })

	if m.Config().DataSource != "" {
		t.Errorf("DataSource = %q, want empty when no CloudConfig is given", m.Config().DataSource)
	}
}
