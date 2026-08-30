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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunRendersSnapshotJSONFromFakeNetBox(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "command-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer command-token" {
			t.Fatalf("Authorization header = %q, want Bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/dcim/devices/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next": nil,
				"results": []map[string]any{
					{
						"id":   1,
						"name": "rack-a-ups",
						"custom_fields": map[string]any{
							"nut_operator": map[string]any{
								"kind":         "UPSDevice",
								"name":         "rack-a-ups",
								"powerDomains": []string{"rack-a"},
								"nut": map[string]any{
									"driver": "dummy-ups",
									"model":  "dummy-loop",
								},
							},
						},
					},
					{
						"id":   2,
						"name": "worker-a",
						"custom_fields": map[string]any{
							"nut_operator": map[string]any{
								"kind":                    "Node",
								"nodeName":                "worker-a",
								"communicationPathExempt": true,
							},
						},
					},
				},
			})
		case "/api/dcim/power-ports/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next": nil,
				"results": []map[string]any{{
					"id":     10,
					"name":   "PSU-A",
					"device": map[string]any{"id": 2, "name": "worker-a"},
					"connected_endpoints": []map[string]any{{
						"id":     20,
						"name":   "Outlet 1",
						"device": map[string]any{"id": 1, "name": "rack-a-ups"},
					}},
				}},
			})
		case "/api/dcim/interfaces/":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next":    nil,
				"results": []map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-url", server.URL,
		"-tag", "power-managed",
		"-format", "snapshot-json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run returned error: %v\nstderr:\n%s", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, `"sourceID": "netbox"`) || !strings.Contains(output, `"observedAt"`) {
		t.Fatalf("unexpected snapshot output:\n%s", output)
	}
	if strings.Contains(output, "command-token") || strings.Contains(stderr.String(), "command-token") {
		t.Fatal("command output must not contain the NetBox token")
	}
}
