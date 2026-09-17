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

func TestRunRejectsUnmappedSecondaryFeedWithoutPartialOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/dcim/devices/":
			_, _ = w.Write([]byte(`{"results":[
				{"id":1,"name":"ups","custom_fields":{"nut_operator":{"kind":"UPSDevice","powerDomains":["test"],"nut":{"driver":"dummy-ups","model":"test"}}}},
				{"id":2,"name":"node","custom_fields":{"nut_operator":{"kind":"Node","communicationPathExempt":true}}}
			]}`))
		case "/api/dcim/power-ports/":
			if r.URL.Query().Get("device_id") == "2" {
				_, _ = w.Write([]byte(`{"results":[
					{"id":10,"name":"PSU-A","device":{"id":2},"connected_endpoints":[{"id":20,"device":{"id":1}}]},
					{"id":11,"name":"PSU-B","device":{"id":2},"connected_endpoints":[{"id":21,"device":{"id":99}}]}
				]}`))
				return
			}
			fallthrough
		default:
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer server.Close()
	for _, format := range []string{"yaml", "snapshot-json"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{"-url", server.URL, "-token-env", "", "-format", format}, &stdout, &stderr)
			if err == nil || stdout.Len() != 0 {
				t.Fatal("unmapped secondary feed must fail without emitting a partial inventory")
			}
			if err.Error() != "NetBox inventory contains an unmapped power endpoint at dcim.PowerPort/11" {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatal("rejection must not expose provider diagnostics before returning the sanitized error")
			}
		})
	}
}
