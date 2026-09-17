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

package netboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const serviceURL = "http://127.0.0.1:8080"

type fixture struct {
	t                *testing.T
	tokens           map[string]string
	http             *http.Client
	unselectedDevice int
	spareInput       int
	nodeFeedCable    int
	interfaceSpares  map[int]int
}

func (f *fixture) request(method, path string, data any, result any) {
	f.t.Helper()
	var body io.Reader
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			f.t.Fatal("encode fixture request")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, serviceURL+"/api/"+path, body)
	if err != nil {
		f.t.Fatal("construct fixture request")
	}
	req.Header.Set("Authorization", "Bearer "+f.tokens["seed"])
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.http.Do(req)
	if err != nil {
		f.t.Fatal("fixture API transport failed")
	}
	defer func() {
		if resp.Body.Close() != nil {
			f.t.Fatal("close fixture response failed (details withheld)")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		f.t.Fatalf("fixture API %s %s returned HTTP %d (body withheld)", method, path, resp.StatusCode)
	}
	if result != nil && json.NewDecoder(resp.Body).Decode(result) != nil {
		f.t.Fatal("decode fixture response")
	}
}

func (f *fixture) create(path string, data any) int {
	f.t.Helper()
	var result struct {
		ID int `json:"id"`
	}
	f.request(http.MethodPost, path, data, &result)
	if result.ID == 0 {
		f.t.Fatal("fixture missing object identity")
	}
	return result.ID
}

func (f *fixture) cable(kindA string, a int, kindB string, b int) int {
	f.t.Helper()
	return f.create("dcim/cables/", map[string]any{
		"a_terminations": []any{map[string]any{"object_type": "dcim." + kindA, "object_id": a}},
		"b_terminations": []any{map[string]any{"object_type": "dcim." + kindB, "object_id": b}}, "status": "connected",
	})
}

func (f *fixture) seed() (int, int) {
	f.create("extras/custom-fields/", map[string]any{"name": "nut_operator", "type": "json", "object_types": []string{"dcim.device"}})
	tag := f.create("extras/tags/", map[string]any{"name": "power-managed", "slug": "power-managed"})
	site := f.create("dcim/sites/", map[string]any{"name": "Disposable", "slug": "disposable"})
	maker := f.create("dcim/manufacturers/", map[string]any{"name": "Synthetic", "slug": "synthetic"})
	typeID := f.create("dcim/device-types/", map[string]any{"manufacturer": maker, "model": "Test", "slug": "test"})
	role := f.create("dcim/device-roles/", map[string]any{"name": "Fixture", "slug": "fixture", "color": "2196f3"})
	device := func(name string, metadata any, tagged bool) int {
		tags := []int{}
		if tagged {
			tags = append(tags, tag)
		}
		return f.create("dcim/devices/", map[string]any{"name": name, "site": site, "role": role, "device_type": typeID,
			"status": "active", "tags": tags, "custom_fields": map[string]any{"nut_operator": metadata}})
	}
	ups := device("provider-ups", map[string]any{"kind": "UPSDevice", "name": "test-ups", "powerDomains": []string{"test-domain"},
		"nut": map[string]any{"driver": "snmp-ups", "endpointHost": "ups.example.invalid", "model": "TEST_UPS"}}, true)
	node := device("provider-node", map[string]any{"kind": "Node", "nodeName": "worker-test", "roles": map[string]any{"shutdownTier": 2}}, true)
	infra := device("provider-switch", map[string]any{"kind": "PowerInfrastructure", "name": "test-switch", "infrastructureClass": "Switch"}, true)
	// This deliberately invalid device must never cross the power-managed filter.
	f.unselectedDevice = device("excluded-invalid", map[string]any{"kind": "Unsupported"}, false)
	for i, target := range []int{node, infra} {
		outlet := f.create("dcim/power-outlets/", map[string]any{"device": ups, "name": fmt.Sprintf("out-%d", i)})
		port := f.create("dcim/power-ports/", map[string]any{"device": target, "name": "PSU-A"})
		cable := f.cable("poweroutlet", outlet, "powerport", port)
		if target == node {
			f.nodeFeedCable = cable
		}
	}
	// More than one port/interface on a device exercises component pagination too.
	f.spareInput = f.create("dcim/power-ports/", map[string]any{"device": node, "name": "PSU-B"})
	// First by both name and creation ID, with no peer to recover a later edge.
	f.interfaceSpares = make(map[int]int)
	for _, target := range []int{infra, node, ups} {
		f.interfaceSpares[target] = f.create("dcim/interfaces/", map[string]any{"device": target, "name": "eth0", "type": "1000base-t"})
	}
	for i, target := range []int{node, ups} {
		local := f.create("dcim/interfaces/", map[string]any{"device": infra, "name": fmt.Sprintf("eth%d", i+1), "type": "1000base-t"})
		peer := f.create("dcim/interfaces/", map[string]any{"device": target, "name": "eth1", "type": "1000base-t"})
		f.cable("interface", local, "interface", peer)
	}
	return node, tag
}

func (f *fixture) cli(token, scheme, format string, extra ...string) ([]byte, []byte, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"-url", serviceURL, "-tag", "power-managed", "-timeout", "20s", "-format", format, "-token-scheme", scheme}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, "/tmp/netbox-inventory-sync", args...)
	// Do not inherit database passwords, service secrets, or ambient site tokens.
	cmd.Env = []string{"NETBOX_TOKEN=" + token}
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	for _, secret := range append([]string{token}, tokenValues(f.tokens)...) {
		if secret != "" && (bytes.Contains(out.Bytes(), []byte(secret)) || bytes.Contains(diagnostic.Bytes(), []byte(secret))) {
			f.t.Fatal("CLI leaked a test credential (output withheld)")
		}
	}
	if ctx.Err() != nil {
		return out.Bytes(), diagnostic.Bytes(), ctx.Err()
	}
	return out.Bytes(), diagnostic.Bytes(), err
}

func tokenValues(tokens map[string]string) []string {
	var values []string
	for _, value := range tokens {
		values = append(values, value)
	}
	return values
}

func (f *fixture) snapshot(extra ...string) inventory.Snapshot {
	f.t.Helper()
	out, _, err := f.cli(f.tokens["read"], "Bearer", "snapshot-json", extra...)
	if err != nil {
		f.t.Fatal("snapshot CLI failed (diagnostics withheld)")
	}
	var snapshot inventory.Snapshot
	if json.Unmarshal(out, &snapshot) != nil {
		f.t.Fatal("invalid snapshot JSON")
	}
	if snapshot.SourceID != "netbox" || snapshot.Version != "v1" {
		f.t.Fatal("snapshot provenance missing")
	}
	if _, err := time.Parse(time.RFC3339, snapshot.ObservedAt); err != nil {
		f.t.Fatal("snapshot timestamp missing")
	}
	// Observation time is intentionally refreshed and participates in the hash.
	snapshot.ObservedAt = ""
	return snapshot
}

func TestRealNetBox(t *testing.T) {
	if os.Getenv("NETBOX_DISPOSABLE_TEST") != "1" {
		t.Skip("run python3 hack/test-netbox.py for disposable real NetBox")
	}
	f := &fixture{t: t, tokens: map[string]string{}, http: &http.Client{Timeout: 10 * time.Second}}
	// Direct phase calls share the parent T: Fatal stops all later mutations.
	f.loadTokens()
	node, tag := f.seed()
	f.assertPagination()
	f.assertSnapshotContract()
	f.assertRenderedContract()
	f.assertAuthenticationFailures()
	f.assertMalformedMetadata(node)
	f.assertUnmappedSupplyAndOrphan(node, tag)
	t.Log("real authentication, pagination, tag filtering, deterministic snapshots/CRs, compilation and fail-closed cases passed")
}

func (f *fixture) loadTokens() {
	t := f.t
	for _, name := range []string{"seed", "read", "legacy"} {
		info, err := os.Stat("/tmp/netbox-" + name + "-token")
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("disposable token file is not private")
		}
		data, err := os.ReadFile("/tmp/netbox-" + name + "-token")
		if err != nil || len(data) == 0 {
			t.Fatal("missing disposable token file")
		}
		f.tokens[name] = string(data)
	}
}

func (f *fixture) assertPagination() {
	t := f.t
	var page struct {
		Count   int               `json:"count"`
		Next    string            `json:"next"`
		Results []json.RawMessage `json:"results"`
	}
	f.request("GET", "dcim/devices/?tag=power-managed&limit=200", nil, &page)
	if page.Count != 3 || len(page.Results) != 1 || page.Next == "" {
		t.Fatal("real server did not force pagination")
	}
	for device, spare := range f.interfaceSpares {
		var interfaces struct {
			Count   int    `json:"count"`
			Next    string `json:"next"`
			Results []struct {
				ID    int             `json:"id"`
				Cable json.RawMessage `json:"cable"`
			} `json:"results"`
		}
		f.request(http.MethodGet, fmt.Sprintf("dcim/interfaces/?device_id=%d&limit=200", device), nil, &interfaces)
		if interfaces.Count < 2 || len(interfaces.Results) != 1 || interfaces.Next == "" {
			t.Fatal("real server did not force interface pagination")
		}
		if interfaces.Results[0].ID != spare || string(interfaces.Results[0].Cable) != "null" {
			t.Fatal("first interface page must contain only the uncabled spare")
		}
	}
}

func (f *fixture) assertSnapshotContract() {
	t := f.t
	snapshot := f.snapshot()
	if len(snapshot.Entities) != 3 || len(snapshot.Edges) != 4 {
		t.Fatal("incomplete or unfiltered topology")
	}
	tier := int32(2)
	expected := inventory.Snapshot{Entities: []inventory.Entity{
		{ID: "test-ups", Kind: inventory.EntityKindUPSDevice, PowerDomains: []string{"test-domain"}, Model: "TEST_UPS", DriverFamily: "snmp-ups"},
		{ID: "worker-test", Kind: inventory.EntityKindNode, ShutdownTier: &tier},
		{ID: "test-switch", Kind: inventory.EntityKindPowerInfrastructure},
	}, Edges: []inventory.Edge{
		{From: "test-ups", To: "worker-test", Relation: inventory.EdgeRelationFeeds, Input: "PSU-A"},
		{From: "test-ups", To: "test-switch", Relation: inventory.EdgeRelationFeeds, Input: "PSU-A"},
		{From: "test-switch", To: "worker-test", Relation: inventory.EdgeRelationCarries},
		{From: "test-switch", To: "test-ups", Relation: inventory.EdgeRelationCarries},
	}}
	actualTopology, diagnostics, err := inventory.Compile(snapshot)
	if err != nil || len(diagnostics) != 0 {
		t.Fatal("real snapshot failed inventory compilation")
	}
	expectedTopology, _, err := inventory.Compile(expected)
	if err != nil {
		t.Fatal("authored reference failed compilation")
	}
	if !reflect.DeepEqual(actualTopology.Domains, expectedTopology.Domains) || !reflect.DeepEqual(actualTopology.Entities, expectedTopology.Entities) {
		t.Fatal("provider inventory differs from authored contract")
	}
	for i := range actualTopology.Edges {
		actualTopology.Edges[i].SourceID = ""
	}
	for i := range actualTopology.CommunicationOrders {
		actualTopology.CommunicationOrders[i].Source = ""
	}
	for i := range expectedTopology.CommunicationOrders {
		expectedTopology.CommunicationOrders[i].Source = ""
	}
	if !reflect.DeepEqual(actualTopology.Edges, expectedTopology.Edges) || !reflect.DeepEqual(actualTopology.CommunicationOrders, expectedTopology.CommunicationOrders) {
		t.Fatal("power/input/communication edge mapping differs from authored contract")
	}
	if !reflect.DeepEqual(snapshot, f.snapshot("-filter", "ordering=-id")) {
		t.Fatal("mapping changes with pagination ordering")
	}
}

func (f *fixture) assertRenderedContract() {
	t := f.t
	first, _, err := f.cli(f.tokens["read"], "Bearer", "yaml")
	if err != nil {
		t.Fatal("YAML CLI failed")
	}
	second, _, err := f.cli(f.tokens["legacy"], "Token", "yaml", "-filter", "ordering=-id")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("legacy authentication or deterministic CR output failed")
	}
	if err := validateRenderedContract(first); err != nil {
		t.Fatal(err)
	}
}

type renderedExpectation struct {
	Kind string
	Spec any
}

// Author the CR contract independently of the provider mapper, including the
// stable edge names. Full typed specs also check fields omitted by this fixture.
func expectedRenderedContract() map[string]renderedExpectation {
	tier := int32(2)
	return map[string]renderedExpectation{
		"test-ups": {"UPSDevice", powerv1alpha1.UPSDeviceSpec{
			DisplayName:  "provider-ups",
			Identity:     powerv1alpha1.UPSDeviceIdentitySpec{Model: "TEST_UPS"},
			Driver:       "snmp-ups",
			Endpoint:     &powerv1alpha1.UPSEndpointSpec{Host: "ups.example.invalid"},
			PowerDomains: []string{"test-domain"},
		}},
		"test-switch": {"PowerInfrastructure", powerv1alpha1.PowerInfrastructureSpec{
			DisplayName: "provider-switch", Class: "Switch",
		}},
		"worker-test": {"PowerInventoryNode", powerv1alpha1.PowerInventoryNodeSpec{
			NodeName: "worker-test", Roles: powerv1alpha1.PowerInventoryNodeRoles{ShutdownTier: &tier},
		}},
		"test-ups-feeds-worker-test-psu-a-aeead3e6": {"PowerInventoryEdge", powerv1alpha1.PowerInventoryEdgeSpec{
			From:     powerv1alpha1.PowerInventoryEntityReference{Kind: "UPSDevice", Name: "test-ups"},
			To:       powerv1alpha1.PowerInventoryEntityReference{Kind: "Node", Name: "worker-test"},
			Relation: "Feeds", Input: "PSU-A",
		}},
		"test-ups-feeds-test-switch-psu-a-8db4ca44": {"PowerInventoryEdge", powerv1alpha1.PowerInventoryEdgeSpec{
			From:     powerv1alpha1.PowerInventoryEntityReference{Kind: "UPSDevice", Name: "test-ups"},
			To:       powerv1alpha1.PowerInventoryEntityReference{Kind: "PowerInfrastructure", Name: "test-switch"},
			Relation: "Feeds", Input: "PSU-A",
		}},
		"test-switch-carries-worker-test-5781da1f": {"PowerInventoryEdge", powerv1alpha1.PowerInventoryEdgeSpec{
			From:     powerv1alpha1.PowerInventoryEntityReference{Kind: "PowerInfrastructure", Name: "test-switch"},
			To:       powerv1alpha1.PowerInventoryEntityReference{Kind: "Node", Name: "worker-test"},
			Relation: "Carries",
		}},
		"test-switch-carries-test-ups-e2d6bef2": {"PowerInventoryEdge", powerv1alpha1.PowerInventoryEdgeSpec{
			From:     powerv1alpha1.PowerInventoryEntityReference{Kind: "PowerInfrastructure", Name: "test-switch"},
			To:       powerv1alpha1.PowerInventoryEntityReference{Kind: "UPSDevice", Name: "test-ups"},
			Relation: "Carries",
		}},
	}
}

func validateRenderedContract(data []byte) error {
	expected := expectedRenderedContract()
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec json.RawMessage `json:"spec"`
		}
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid rendered CR (details withheld)")
		}
		want, ok := expected[object.Metadata.Name]
		if !ok {
			return errors.New("unexpected or duplicate rendered CR")
		}
		if object.APIVersion != "power.zalud.io/v1alpha1" || object.Kind != want.Kind {
			return errors.New("rendered CR type differs from authored contract")
		}
		// Decode the entire spec strictly into its API type; unknown fields must
		// not disappear before the exact comparison.
		actual := reflect.New(reflect.TypeOf(want.Spec))
		specDecoder := json.NewDecoder(bytes.NewReader(object.Spec))
		specDecoder.DisallowUnknownFields()
		if specDecoder.Decode(actual.Interface()) != nil || !reflect.DeepEqual(actual.Elem().Interface(), want.Spec) {
			return errors.New("rendered CR spec differs from authored contract")
		}
		delete(expected, object.Metadata.Name)
	}
	if len(expected) != 0 {
		return errors.New("missing rendered CRs")
	}
	return nil
}

func TestRenderedContractRejectsCorruption(t *testing.T) {
	const edgeName = "test-ups-feeds-worker-test-psu-a-aeead3e6"
	for _, tc := range []struct {
		name   string
		mutate func(map[string]renderedExpectation)
	}{
		{"edge relation", func(objects map[string]renderedExpectation) {
			object := objects[edgeName]
			spec := object.Spec.(powerv1alpha1.PowerInventoryEdgeSpec)
			spec.Relation = "Carries"
			object.Spec = spec
			objects[edgeName] = object
		}},
		{"edge endpoint name", func(objects map[string]renderedExpectation) {
			object := objects[edgeName]
			spec := object.Spec.(powerv1alpha1.PowerInventoryEdgeSpec)
			spec.To.Name = "test-switch"
			object.Spec = spec
			objects[edgeName] = object
		}},
		{"edge endpoint kind", func(objects map[string]renderedExpectation) {
			object := objects[edgeName]
			spec := object.Spec.(powerv1alpha1.PowerInventoryEdgeSpec)
			spec.To.Kind = "UPSDevice"
			object.Spec = spec
			objects[edgeName] = object
		}},
		{"lost UPS configuration", func(objects map[string]renderedExpectation) {
			objects["test-ups"] = renderedExpectation{"UPSDevice", powerv1alpha1.UPSDeviceSpec{}}
		}},
		{"extra object", func(objects map[string]renderedExpectation) {
			objects["unexpected-node"] = objects["worker-test"]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := expectedRenderedContract()
			if err := validateRenderedContract(encodeAuthoredCRs(t, objects)); err != nil {
				t.Fatal("assertion helper rejected authored baseline")
			}
			tc.mutate(objects)
			if validateRenderedContract(encodeAuthoredCRs(t, objects)) == nil {
				t.Fatal("assertion helper accepted corrupted CRs")
			}
		})
	}
	t.Run("duplicate object", func(t *testing.T) {
		data := encodeAuthoredCRs(t, expectedRenderedContract())
		data = append(data, encodeAuthoredCRs(t, expectedRenderedContract())...)
		if validateRenderedContract(data) == nil {
			t.Fatal("assertion helper accepted duplicate CRs")
		}
	})
}

func encodeAuthoredCRs(t *testing.T, objects map[string]renderedExpectation) []byte {
	t.Helper()
	var output bytes.Buffer
	for name, object := range objects {
		// A YAML document marker forces YAML stream decoding even though each
		// document uses JSON syntax (a YAML subset).
		output.WriteString("---\n")
		err := json.NewEncoder(&output).Encode(map[string]any{
			"apiVersion": "power.zalud.io/v1alpha1", "kind": object.Kind,
			"metadata": map[string]string{"name": name}, "spec": object.Spec,
		})
		if err != nil {
			t.Fatal("encode authored CR fixture")
		}
	}
	return output.Bytes()
}

func (f *fixture) assertAuthenticationFailures() {
	t := f.t
	for _, token := range []string{"", "invalid-disposable-credential"} {
		out, diagnostic, err := f.cli(token, "Bearer", "snapshot-json")
		if err == nil || len(out) != 0 || (!bytes.Contains(diagnostic, []byte("403")) && !bytes.Contains(diagnostic, []byte("401"))) {
			t.Fatal("real API did not fail closed on missing/invalid authentication")
		}
	}
}

func (f *fixture) assertMalformedMetadata(node int) {
	path := fmt.Sprintf("dcim/devices/%d/", node)
	for _, tc := range []struct {
		name        string
		metadata    any
		diagnostics []string
	}{
		{"array shape", []string{"invalid-metadata-shape"}, []string{`decode custom field "nut_operator"`, "cannot unmarshal array"}},
		{"unsupported kind", map[string]any{"kind": "Unsupported"}, []string{`declares unsupported nut-operator kind "Unsupported"`}},
		{"missing nut metadata", map[string]any{"kind": "UPSDevice"}, []string{"declares UPSDevice but omits nut metadata"}},
	} {
		f.request("PATCH", path, map[string]any{"custom_fields": map[string]any{"nut_operator": tc.metadata}}, nil)
		for _, format := range []string{"snapshot-json", "yaml"} {
			out, diagnostic, err := f.cli(f.tokens["read"], "Bearer", format)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(out) != 0 {
				f.t.Fatalf("%s (%s): expected ordinary CLI failure with empty stdout", tc.name, format)
			}
			for _, expected := range tc.diagnostics {
				if !bytes.Contains(diagnostic, []byte(expected)) {
					f.t.Fatalf("%s (%s): missing expected diagnostic (output withheld)", tc.name, format)
				}
			}
		}
	}
}

func (f *fixture) assertUnmappedSupplyAndOrphan(node, tag int) {
	t := f.t
	path := fmt.Sprintf("dcim/devices/%d/", node)
	// Restore valid metadata. A second, unselected supply must not be silently dropped.
	f.request("PATCH", path, map[string]any{"custom_fields": map[string]any{"nut_operator": map[string]any{"kind": "Node", "nodeName": "worker-test"}}, "tags": []int{tag}}, nil)
	outlet := f.create("dcim/power-outlets/", map[string]any{"device": f.unselectedDevice, "name": "unmapped-source"})
	secondaryCable := f.cable("poweroutlet", outlet, "powerport", f.spareInput)
	for _, format := range []string{"snapshot-json", "yaml"} {
		out, diagnostic, err := f.cli(f.tokens["read"], "Bearer", format)
		if err == nil || len(out) != 0 || !strings.Contains(string(diagnostic), "unmapped power endpoint") {
			t.Fatal("unmapped secondary feed produced partial inventory")
		}
	}
	// Removing both cables leaves an orphan, exercising the compiler rejection too.
	for _, cable := range []int{secondaryCable, f.nodeFeedCable} {
		f.request("DELETE", fmt.Sprintf("dcim/cables/%d/", cable), nil, nil)
	}
	out, diagnostic, err := f.cli(f.tokens["read"], "Bearer", "snapshot-json")
	if err == nil || len(out) != 0 || !strings.Contains(string(diagnostic), "not structurally valid") {
		t.Fatal("unmapped feed did not fail inventory compilation closed")
	}
}
