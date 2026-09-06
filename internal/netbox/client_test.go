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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/inventory"
)

func TestFetchAndBuildManifestFromFakeNetBox(t *testing.T) {
	server := fakeNetBoxServer(t)
	defer server.Close()

	client, err := NewClient(ClientOptions{
		URL:         server.URL,
		Token:       "test-token",
		TokenScheme: TokenSchemeBearer,
		HTTPClient:  server.Client(),
		PageLimit:   2,
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	query := url.Values{}
	query.Add("tag", "power-managed")
	source, err := client.Fetch(context.Background(), FetchOptions{
		DeviceFilters: query,
		ObservedAt:    time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(source.Devices) != 3 {
		t.Fatalf("expected three devices across paginated responses, got %d", len(source.Devices))
	}

	manifest, err := BuildManifest(source, MappingOptions{})
	if err != nil {
		t.Fatalf("BuildManifest returned error: %v", err)
	}
	if len(manifest.UPSDevices) != 1 || len(manifest.PowerInventoryNodes) != 1 || len(manifest.PowerInfrastructure) != 1 {
		t.Fatalf("unexpected object counts: UPS=%d nodes=%d infra=%d", len(manifest.UPSDevices), len(manifest.PowerInventoryNodes), len(manifest.PowerInfrastructure))
	}
	if len(manifest.PowerInventoryEdges) != 3 {
		t.Fatalf("expected feeds plus two carries edges, got %#v", manifest.PowerInventoryEdges)
	}
	if _, diagnostics, err := inventory.Compile(manifest.Snapshot); err != nil {
		t.Fatalf("rendered snapshot must compile, got %v with diagnostics %#v", err, diagnostics)
	}

	edgeIDs := edgeIDs(manifest.Snapshot.Edges)
	for _, want := range []string{
		"rack-a-ups|worker-a|feeds|PSU-A",
		"rack-a-switch|rack-a-ups|carries|",
		"rack-a-switch|worker-a|carries|",
	} {
		if !slices.Contains(edgeIDs, want) {
			t.Fatalf("expected edge %q in %#v", want, edgeIDs)
		}
	}

	rendered, err := manifest.YAML()
	if err != nil {
		t.Fatalf("YAML returned error: %v", err)
	}
	renderedText := string(rendered)
	for _, want := range []string{"kind: UPSDevice", "kind: PowerInventoryEdge", "rack-a-switch-carries-worker-a"} {
		if !strings.Contains(renderedText, want) {
			t.Fatalf("rendered YAML did not contain %q:\n%s", want, renderedText)
		}
	}
	if strings.Contains(renderedText, "test-token") {
		t.Fatal("rendered inventory must not contain the NetBox token")
	}
}

func TestBuildManifestRejectsIncompleteUPSMetadata(t *testing.T) {
	source := Source{
		ObservedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		Devices: []Device{{
			ID:   1,
			Name: "rack-a-ups",
			CustomFields: map[string]json.RawMessage{
				DefaultOperatorCustomField: rawJSON(t, map[string]any{
					"kind":         "UPSDevice",
					"powerDomains": []string{"rack-a"},
					"nut": map[string]any{
						"driver": "snmp-ups",
					},
				}),
			},
		}},
	}

	_, err := BuildManifest(source, MappingOptions{})
	if err == nil || !strings.Contains(err.Error(), "endpointHost") {
		t.Fatalf("expected missing endpointHost error, got %v", err)
	}
}

func TestBuildManifestWarnsForPowerEndpointOutsideImportedSet(t *testing.T) {
	source := Source{
		ObservedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		Devices: []Device{
			nodeDevice(t),
			upsDevice(t),
		},
		PowerPorts: []PowerPort{{
			ID:     10,
			Name:   "PSU-A",
			Device: BriefObject{ID: 2, Name: "worker-a"},
			ConnectedEndpoints: []Endpoint{{
				ID:     99,
				Name:   "Outlet 1",
				Device: &BriefObject{ID: 99, Name: "unimported-pdu"},
			}},
		}},
	}

	manifest, err := BuildManifest(source, MappingOptions{})
	if err != nil {
		t.Fatalf("BuildManifest returned error: %v", err)
	}
	if len(manifest.Diagnostics) != 1 || manifest.Diagnostics[0].Reason != "PowerEndpointUnmapped" {
		t.Fatalf("expected unmapped endpoint warning, got %#v", manifest.Diagnostics)
	}
}

func TestFetchRejectsPaginationOutsideNetBoxOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dcim/devices/" {
			t.Fatalf("unexpected request after cross-origin pagination URL: %s", r.URL.String())
		}
		writePage[Device](t, w, "https://elsewhere.example/api/dcim/devices/?offset=2", nil)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		URL:         server.URL,
		Token:       "test-token",
		TokenScheme: TokenSchemeBearer,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	_, err = client.Fetch(context.Background(), FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not match configured NetBox origin") {
		t.Fatalf("expected cross-origin pagination rejection, got %v", err)
	}
}

func TestFetchRejectsPaginationLoops(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writePage[Device](t, w, serverURL(r), nil)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		URL:        server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	_, err = client.Fetch(context.Background(), FetchOptions{})
	if err == nil || !strings.Contains(err.Error(), "pagination loop") {
		t.Fatalf("expected pagination loop rejection, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("pagination loop should be rejected before rerequesting the same URL, got %d requests", requests)
	}
}

func TestNewClientRejectsUnsupportedURLSchemes(t *testing.T) {
	_, err := NewClient(ClientOptions{URL: "ssh://netbox.example.test"})
	if err == nil || !strings.Contains(err.Error(), "scheme must be http or https") {
		t.Fatalf("expected unsupported scheme rejection, got %v", err)
	}
}

func fakeNetBoxServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization header = %q, want Bearer token", got)
		}
		switch r.URL.Path {
		case "/api/dcim/devices/":
			if got := r.URL.Query()["tag"]; !slices.Contains(got, "power-managed") {
				t.Fatalf("device query tag = %#v, want power-managed", got)
			}
			if r.URL.Query().Get("offset") == "" {
				writePage(t, w, server.URL+"/api/dcim/devices/?limit=2&offset=2&tag=power-managed", []Device{
					upsDevice(t),
					nodeDevice(t),
				})
				return
			}
			writePage(t, w, "", []Device{switchDevice(t)})
		case "/api/dcim/power-ports/":
			switch r.URL.Query().Get("device_id") {
			case "2":
				writePage(t, w, "", []PowerPort{{
					ID:     10,
					Name:   "PSU-A",
					Device: BriefObject{ID: 2, Name: "worker-a"},
					ConnectedEndpoints: []Endpoint{{
						ID:     20,
						Name:   "Outlet 1",
						Device: &BriefObject{ID: 1, Name: "rack-a-ups"},
					}},
				}})
			default:
				writePage(t, w, "", []PowerPort{})
			}
		case "/api/dcim/interfaces/":
			switch r.URL.Query().Get("device_id") {
			case "1":
				writePage(t, w, "", []Interface{{
					ID:     30,
					Name:   "mgmt0",
					Device: BriefObject{ID: 1, Name: "rack-a-ups"},
					ConnectedEndpoints: []Endpoint{{
						ID:     31,
						Name:   "Ethernet1",
						Device: &BriefObject{ID: 3, Name: "rack-a-switch"},
					}},
				}})
			case "2":
				writePage(t, w, "", []Interface{{
					ID:     32,
					Name:   "eth0",
					Device: BriefObject{ID: 2, Name: "worker-a"},
					ConnectedEndpoints: []Endpoint{{
						ID:     33,
						Name:   "Ethernet2",
						Device: &BriefObject{ID: 3, Name: "rack-a-switch"},
					}},
				}})
			default:
				writePage(t, w, "", []Interface{})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func upsDevice(t *testing.T) Device {
	t.Helper()
	return Device{
		ID:         1,
		Name:       "rack-a-ups",
		Display:    "Rack A UPS",
		DeviceType: &DeviceType{Model: "TOWER_1000VA_230V"},
		CustomFields: map[string]json.RawMessage{
			DefaultOperatorCustomField: rawJSON(t, map[string]any{
				"kind":         "UPSDevice",
				"name":         "rack-a-ups",
				"powerDomains": []string{"rack-a"},
				"nut": map[string]any{
					"driver":       "snmp-ups",
					"endpointHost": "ups-a.example.test",
					"endpointPort": 161,
					"model":        "TOWER_1000VA_230V",
				},
			}),
		},
	}
}

func nodeDevice(t *testing.T) Device {
	t.Helper()
	return Device{
		ID:      2,
		Name:    "worker-a",
		Display: "Worker A",
		CustomFields: map[string]json.RawMessage{
			DefaultOperatorCustomField: rawJSON(t, map[string]any{
				"kind":     "Node",
				"nodeName": "worker-a",
				"roles": map[string]any{
					"shutdownTier": 2,
				},
			}),
		},
	}
}

func switchDevice(t *testing.T) Device {
	t.Helper()
	return Device{
		ID:      3,
		Name:    "rack-a-switch",
		Display: "Rack A Switch",
		Role:    &BriefObject{Slug: "switch", Name: "Switch"},
	}
}

func writePage[T any](t *testing.T, w http.ResponseWriter, next string, results []T) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"count":   len(results),
		"next":    next,
		"results": results,
	}); err != nil {
		t.Fatalf("encode page: %v", err)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host + r.URL.RequestURI()
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw json: %v", err)
	}
	return encoded
}

func edgeIDs(edges []inventory.Edge) []string {
	ids := make([]string, 0, len(edges))
	for _, edge := range edges {
		ids = append(ids, strings.Join([]string{edge.From, edge.To, string(edge.Relation), edge.Input}, "|"))
	}
	slices.Sort(ids)
	return ids
}
