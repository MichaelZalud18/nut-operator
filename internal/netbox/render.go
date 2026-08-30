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

package netbox

import (
	"bytes"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

// Objects returns all rendered Kubernetes resources in dependency-friendly
// order. The API server does not require this order, but humans reading a
// manifest do.
func (m Manifest) Objects() []any {
	objects := make([]any, 0, len(m.UPSDevices)+len(m.PowerInfrastructure)+len(m.PowerInventoryNodes)+len(m.PowerInventoryEdges))
	for i := range m.UPSDevices {
		objects = append(objects, m.UPSDevices[i])
	}
	for i := range m.PowerInfrastructure {
		objects = append(objects, m.PowerInfrastructure[i])
	}
	for i := range m.PowerInventoryNodes {
		objects = append(objects, m.PowerInventoryNodes[i])
	}
	for i := range m.PowerInventoryEdges {
		objects = append(objects, m.PowerInventoryEdges[i])
	}
	return objects
}

// YAML renders the imported inventory as applyable multi-document YAML.
func (m Manifest) YAML() ([]byte, error) {
	var out bytes.Buffer
	for i, object := range m.Objects() {
		if i > 0 {
			out.WriteString("---\n")
		}
		encoded, err := yaml.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("render inventory object: %w", err)
		}
		out.Write(encoded)
	}
	return out.Bytes(), nil
}

// SnapshotJSON renders the provider-neutral inventory snapshot for debugging.
func (m Manifest) SnapshotJSON() ([]byte, error) {
	return json.MarshalIndent(m.Snapshot, "", "  ")
}
