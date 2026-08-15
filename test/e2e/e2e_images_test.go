//go:build e2e
// +build e2e

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

package e2e

import "testing"

// The image table is five near-identical rows, which is exactly the shape where a copy-paste slip
// is invisible: two rows sharing a SourceEnv would silently test one image twice and never test the
// other, and CI would pull the wrong artifact while reporting success (F-77).
func TestSuiteImageTableIsWellFormed(t *testing.T) {
	images := suiteImages()
	if len(images) == 0 {
		t.Fatal("the suite declares no images to load")
	}

	localRefs := map[string]string{}
	sourceEnvs := map[string]string{}
	for _, image := range images {
		if image.LocalRef == "" || image.SourceEnv == "" || image.BuildTarget == "" || len(image.BuildVars) == 0 {
			t.Errorf("%+v is missing a field; an incomplete row cannot both build and pull", image)
			continue
		}
		if previous, seen := localRefs[image.LocalRef]; seen {
			t.Errorf("local ref %q is claimed by both %q and %q", image.LocalRef, previous, image.SourceEnv)
		}
		localRefs[image.LocalRef] = image.SourceEnv
		if previous, seen := sourceEnvs[image.SourceEnv]; seen {
			t.Errorf("%s would supply both %q and %q; one of them can never be overridden",
				image.SourceEnv, previous, image.LocalRef)
		}
		sourceEnvs[image.SourceEnv] = image.LocalRef
	}

	// Every image the specs reference has to be in the table, or it is never loaded into Kind.
	for _, required := range []string{managerImage, nutServerImage, upsmonAgentImage, nodeActuatorImage, snmpsimFixtureImage} {
		if _, present := localRefs[required]; !present {
			t.Errorf("%s is used by the specs but is not in the image table", required)
		}
	}
}
