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

package capability

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBundledCatalogManifestMatchesCodeCatalog(t *testing.T) {
	manifestProfiles := readCatalogManifestProfiles(t)
	if len(manifestProfiles) != len(BundledProfiles()) {
		t.Fatalf("catalog manifest profile count = %d, want %d", len(manifestProfiles), len(BundledProfiles()))
	}

	byID := map[string]manifestCapabilityProfile{}
	for _, profile := range manifestProfiles {
		byID[profile.Metadata.Name] = profile
	}

	for _, bundled := range BundledProfiles() {
		manifest, ok := byID[bundled.ID]
		if !ok {
			t.Fatalf("catalog manifest missing bundled profile %q", bundled.ID)
		}
		if manifest.Kind != "UPSCapabilityProfile" {
			t.Fatalf("manifest profile %q has kind %q", bundled.ID, manifest.Kind)
		}
		if manifest.Spec.Version != bundled.Version {
			t.Fatalf("manifest profile %q version = %q, want %q", bundled.ID, manifest.Spec.Version, bundled.Version)
		}
		if manifest.Spec.Selector.Model != bundled.Selector.Model ||
			manifest.Spec.Selector.Firmware != bundled.Selector.Firmware ||
			manifest.Spec.Selector.ModelGlob != bundled.Selector.ModelGlob ||
			manifest.Spec.Selector.DriverFamily != bundled.Selector.DriverFamily ||
			manifestUniversal(manifest) != bundled.Selector.Universal {
			t.Fatalf("manifest profile %q selector = %#v, want %#v", bundled.ID, manifest.Spec.Selector, bundled.Selector)
		}
		if !reflect.DeepEqual(manifest.Spec.Telemetry.Variables, bundled.TelemetryVariables) {
			t.Fatalf("manifest profile %q telemetry = %#v, want %#v", bundled.ID, manifest.Spec.Telemetry.Variables, bundled.TelemetryVariables)
		}
		if !reflect.DeepEqual(manifest.Spec.Actuation.Behaviors, bundled.ActuationBehaviors) {
			t.Fatalf("manifest profile %q actuation = %#v, want %#v", bundled.ID, manifest.Spec.Actuation.Behaviors, bundled.ActuationBehaviors)
		}
		if !reflect.DeepEqual(manifest.Spec.Quirks, bundled.Quirks) {
			t.Fatalf("manifest profile %q quirks = %#v, want %#v", bundled.ID, manifest.Spec.Quirks, bundled.Quirks)
		}
	}
}

func readCatalogManifestProfiles(t *testing.T) []manifestCapabilityProfile {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate current test file")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "config", "catalog", "upscapabilityprofiles.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read catalog manifest: %v", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var profiles []manifestCapabilityProfile
	for {
		var profile manifestCapabilityProfile
		err := decoder.Decode(&profile)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode catalog manifest: %v", err)
		}
		if profile.Kind == "" {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func manifestUniversal(profile manifestCapabilityProfile) bool {
	return profile.Spec.Selector.Universal != nil && *profile.Spec.Selector.Universal
}

type manifestCapabilityProfile struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Version  string `yaml:"version"`
		Selector struct {
			Model        string `yaml:"model"`
			Firmware     string `yaml:"firmware"`
			ModelGlob    string `yaml:"modelGlob"`
			DriverFamily string `yaml:"driverFamily"`
			Universal    *bool  `yaml:"universal"`
		} `yaml:"selector"`
		Telemetry struct {
			Variables []string `yaml:"variables"`
		} `yaml:"telemetry"`
		Actuation struct {
			Behaviors []string `yaml:"behaviors"`
		} `yaml:"actuation"`
		Quirks []string `yaml:"quirks"`
	} `yaml:"spec"`
}
