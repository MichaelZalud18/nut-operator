package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func TestProfileBundle(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.Command(filepath.Join(root, "bin", "kustomize"), "build", filepath.Join(root, "config", "byo-cert"))
	source, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := build(bytes.NewReader(source), &out, "example.com/manager:check", "example.com/nut:check"); err != nil {
		t.Fatal(err)
	}
	data := out.String()
	for _, denied := range []string{"nodepoweragents", "shutdownflows", "powermanagementclusters", "powerinventory", "upscapability", "pducapability", "resources:\n  - nodes", "cert-manager.io/v1", "--profile=full"} {
		if strings.Contains(data, denied) {
			t.Errorf("profile includes %q", denied)
		}
	}
	for _, required := range []string{"--profile=nut-only", "--nut-server-image=example.com/nut:check", "image: example.com/manager:check", "upsdevices.power.zalud.io", "nutservers.power.zalud.io", "nutservers/status", "namespaceSelector", "leases"} {
		if !strings.Contains(data, required) {
			t.Errorf("profile omits %q", required)
		}
	}
	if strings.Count(data, "kind: CustomResourceDefinition") != 2 {
		t.Fatal("profile must have exactly two CRDs")
	}
	if err := build(bytes.NewReader(source), &out, "manager", ""); err == nil {
		t.Fatal("missing default operand accepted")
	}
}

func TestRestrictRulesPreservesNames(t *testing.T) {
	source := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets", "nodes"}, ResourceNames: []string{"specific-secret"}, Verbs: []string{"get", "patch", "delete", "impersonate"}}}
	rules := restrictRules(source)
	if len(rules) != 1 || len(rules[0].Resources) != 1 || rules[0].Resources[0] != "secrets" || strings.Join(rules[0].Verbs, ",") != "get,patch" || strings.Join(rules[0].ResourceNames, ",") != "specific-secret" {
		t.Fatalf("profile widened source permissions: %+v", rules)
	}
}
