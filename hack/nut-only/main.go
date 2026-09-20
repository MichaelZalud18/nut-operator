// Command nut-only derives the standalone installer from generated full-install resources.
// CRD schemas and webhook entries stay owned by controller-gen; profile RBAC is an explicit
// intersection with that generated role, never a second hand-maintained copy of its rules.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

var profilePermissions = map[string][]string{
	"power.zalud.io/nutservers":            {"get", "list", "watch", "update", "patch"},
	"power.zalud.io/nutservers/status":     {"get", "update", "patch"},
	"power.zalud.io/nutservers/finalizers": {"update"},
	"power.zalud.io/upsdevices":            {"get", "list", "watch"},
	"/namespaces":                          {"get", "list", "watch", "create", "update", "patch"},
	"/configmaps":                          {"get", "list", "watch", "create", "update", "patch"},
	"/secrets":                             {"get", "list", "watch", "create", "update", "patch"},
	"/services":                            {"get", "list", "watch", "create", "update", "patch"},
	"/events":                              {"create", "patch"},
	"events.k8s.io/events":                 {"create", "patch"},
	"apps/deployments":                     {"get", "list", "watch", "create", "update", "patch"},
	"networking.k8s.io/networkpolicies":    {"get", "list", "watch", "create", "update", "patch"},
	"policy/poddisruptionbudgets":          {"get", "list", "watch", "create", "update", "patch"},
}

func main() {
	image := flag.String("image", "", "Manager image")
	operand := flag.String("nut-server-image", "", "Default NUT operand image")
	flag.Parse()
	if err := build(os.Stdin, os.Stdout, *image, *operand); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(in io.Reader, out io.Writer, image, operand string) error {
	if image == "" || operand == "" {
		return fmt.Errorf("both manager and NUT operand image references are required")
	}
	reader := yamlutil.NewYAMLReader(bufio.NewReader(in))
	counts := map[string]int{}
	// Buffer output so a failed derivation never publishes a partial installer.
	var documents [][]byte
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
			return err
		}
		keep, err := selectResource(obj, image, operand)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		counts[obj.GetKind()]++
		encoded, err := yaml.Marshal(obj.Object)
		if err != nil {
			return err
		}
		documents = append(documents, encoded)
	}
	for kind, count := range map[string]int{"CustomResourceDefinition": 2, "Deployment": 1, "MutatingWebhookConfiguration": 1, "ValidatingWebhookConfiguration": 1, "ClusterRole": 2, "ClusterRoleBinding": 2, "ServiceAccount": 1} {
		if counts[kind] != count {
			return fmt.Errorf("profile requires %d %s resources, got %d", count, kind, counts[kind])
		}
	}
	for _, doc := range documents {
		if _, err := fmt.Fprintf(out, "---\n%s", doc); err != nil {
			return err
		}
	}
	return nil
}

func selectResource(obj *unstructured.Unstructured, image, operand string) (bool, error) {
	switch obj.GetKind() {
	case "CustomResourceDefinition":
		return slices.Contains([]string{"upsdevices.power.zalud.io", "nutservers.power.zalud.io"}, obj.GetName()), nil
	case "ClusterRole":
		if obj.GetName() == "nut-operator-metrics-auth-role" {
			return true, nil
		}
		if obj.GetName() != "nut-operator-manager-role" {
			return false, nil
		}
		var role rbacv1.ClusterRole
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &role); err != nil {
			return false, err
		}
		role.Rules = restrictRules(role.Rules)
		var err error
		obj.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(&role)
		return true, err
	case "ClusterRoleBinding":
		return slices.Contains([]string{"nut-operator-manager-rolebinding", "nut-operator-metrics-auth-rolebinding"}, obj.GetName()), nil
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		hooks, _, err := unstructured.NestedSlice(obj.Object, "webhooks")
		if err != nil {
			return false, err
		}
		var selected []any
		for _, hook := range hooks {
			h := hook.(map[string]any)
			path, _, err := unstructured.NestedString(h, "clientConfig", "service", "path")
			if err != nil {
				return false, err
			}
			if slices.Contains([]string{"/mutate-power-zalud-io-v1alpha1-upsdevice", "/validate-power-zalud-io-v1alpha1-upsdevice", "/mutate-power-zalud-io-v1alpha1-nutserver", "/validate-power-zalud-io-v1alpha1-nutserver"}, path) {
				selected = append(selected, h)
			}
		}
		if len(selected) != 2 {
			return false, fmt.Errorf("expected device/server webhooks in %s", obj.GetKind())
		}
		return true, unstructured.SetNestedSlice(obj.Object, selected, "webhooks")
	case "Deployment":
		containers, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
		if err != nil {
			return false, err
		}
		if len(containers) != 1 {
			return false, fmt.Errorf("unexpected manager container layout")
		}
		container := containers[0].(map[string]any)
		container["image"] = image
		args, _, err := unstructured.NestedStringSlice(container, "args")
		if err != nil {
			return false, err
		}
		args = append(args, "--profile=nut-only", "--nut-server-image="+operand)
		if err := unstructured.SetNestedStringSlice(container, args, "args"); err != nil {
			return false, err
		}
		return true, unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers")
	case "Namespace", "ServiceAccount", "Role", "RoleBinding", "Service", "NetworkPolicy", "PodDisruptionBudget":
		return true, nil
	default:
		return false, nil
	}
}

func restrictRules(source []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	var result []rbacv1.PolicyRule
	for _, rule := range source {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				var verbs []string
				for _, verb := range rule.Verbs {
					if slices.Contains(profilePermissions[group+"/"+resource], verb) {
						verbs = append(verbs, verb)
					}
				}
				if len(verbs) > 0 {
					result = append(result, rbacv1.PolicyRule{APIGroups: []string{group}, Resources: []string{resource}, ResourceNames: slices.Clone(rule.ResourceNames), Verbs: verbs})
				}
			}
		}
	}
	return result
}
