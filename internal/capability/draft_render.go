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
	"fmt"
	"sort"
	"strings"
)

// ProfileYAML renders the draft as an applyable UPSCapabilityProfile.
//
// Only variable names are emitted, never readings. A capability profile
// describes a product model, so copying one unit's measured values into it
// would be wrong on its face and would leak site data into a manifest users are
// encouraged to contribute upstream.
func (d Draft) ProfileYAML() string {
	var out strings.Builder

	out.WriteString("apiVersion: power.zalud.io/v1alpha1\n")
	out.WriteString("kind: UPSCapabilityProfile\n")
	out.WriteString("metadata:\n")
	fmt.Fprintf(&out, "  name: %s\n", d.ProfileName)
	out.WriteString("spec:\n")
	out.WriteString("  version: \"0.1.0\"\n")
	out.WriteString("  selector:\n")
	if d.Model == "" {
		out.WriteString("    # REQUIRED: the device reported no ups.model, so nothing can be filled in here.\n")
		out.WriteString("    # Without a selector this profile will never match anything.\n")
		out.WriteString("    model: \"\"\n")
	} else {
		fmt.Fprintf(&out, "    model: %s\n", quoteYAMLScalar(d.Model))
		if d.Firmware != "" {
			out.WriteString("    # Uncomment to narrow this profile to the firmware it was verified against.\n")
			fmt.Fprintf(&out, "    # firmware: %s\n", quoteYAMLScalar(d.Firmware))
		}
	}

	out.WriteString("  telemetry:\n")
	out.WriteString("    variables:\n")
	for _, variable := range d.Variables {
		fmt.Fprintf(&out, "      - %s\n", variable)
	}
	if len(d.SuggestedAliases) > 0 {
		out.WriteString("    # SUGGESTED, NOT VERIFIED. Each mapping below is a guess that a standard\n")
		out.WriteString("    # reading arrived under a non-standard name. Confirm against the device's\n")
		out.WriteString("    # documentation before keeping any of them.\n")
		out.WriteString("    aliases:\n")
		for _, source := range sortedAliasSources(d.SuggestedAliases) {
			fmt.Fprintf(&out, "      %s: %s\n", source, d.SuggestedAliases[source])
		}
	}

	out.WriteString("  actuation:\n")
	out.WriteString("    # Intentionally empty. Actuation commands can cut power to equipment, so\n")
	out.WriteString("    # support is declared only after verification against the firmware in use.\n")
	out.WriteString("    behaviors: []\n")

	return out.String()
}

// IssueReport renders the same findings for a GitHub issue, so a user who has
// verified a device can contribute the profile back to the catalog.
func (d Draft) IssueReport() string {
	var out strings.Builder

	out.WriteString("### Device\n\n")
	fmt.Fprintf(&out, "- Manufacturer: `%s`\n", orUnreported(d.Manufacturer))
	fmt.Fprintf(&out, "- Model: `%s`\n", orUnreported(d.Model))
	fmt.Fprintf(&out, "- Firmware: `%s`\n", orUnreported(d.Firmware))
	fmt.Fprintf(&out, "- Reported variables: %d\n", len(d.Variables))

	out.WriteString("\n### Proposed profile\n\n```yaml\n")
	out.WriteString(d.ProfileYAML())
	out.WriteString("```\n")

	if len(d.NonStandardVariables) > 0 {
		out.WriteString("\n### Non-standard variable names\n\n")
		for _, variable := range d.NonStandardVariables {
			fmt.Fprintf(&out, "- `%s`\n", variable)
		}
	}

	if len(d.SuggestedAliases) > 0 {
		out.WriteString("\n### Suggested aliases (unverified)\n\n")
		for _, source := range sortedAliasSources(d.SuggestedAliases) {
			fmt.Fprintf(&out, "- `%s` -> `%s`\n", source, d.SuggestedAliases[source])
		}
	}

	out.WriteString("\n### Notes\n\n")
	for _, note := range d.Notes {
		fmt.Fprintf(&out, "- %s\n", note)
	}

	out.WriteString("\n### Verification\n\n")
	out.WriteString("- [ ] The model string above is what this product reports on current firmware\n")
	out.WriteString("- [ ] The variable list was read from a healthy, fully connected driver\n")
	out.WriteString("- [ ] Any suggested alias was confirmed against vendor or NUT documentation\n")
	out.WriteString("- [ ] No site-specific values are included in this report\n")

	return out.String()
}

// sortedAliasSources gives the alias map a stable render order. Go map
// iteration is randomized, and a draft that changes shape between reads is a
// draft nobody can diff or trust.
func sortedAliasSources(aliases map[string]string) []string {
	sources := make([]string, 0, len(aliases))
	for source := range aliases {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources
}

func orUnreported(value string) string {
	if value == "" {
		return "not reported"
	}
	return value
}

// quoteYAMLScalar quotes a model or firmware string when it could otherwise be
// parsed as a non-string scalar or break the document.
func quoteYAMLScalar(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, ":#{}[],&*?|<>=!%@`\"'\n") || strings.TrimSpace(value) != value {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
	}
	switch strings.ToLower(value) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return `"` + value + `"`
	}
	return value
}
