#!/usr/bin/env python3
"""Turn an ASH scan result into a triage report naming every actionable finding.

ASH already decides pass/fail and prints a per-scanner count table. What it does not print is
*which* findings failed it, so a red build says "grype: 2 critical" and the only way to learn what
those two are is to download the report artifact and read SARIF by hand. That is the gap this
closes: on a failing scan the job log and the GitHub step summary name the rule, the severity, the
file, and for dependency findings the package and version, which is enough to triage without
leaving the page.

Actionable means unsuppressed and at or above the configured severity threshold -- the same
definition ASH uses for its own verdict. That overlap is deliberate and load-bearing, so the script
reconciles its extraction against the per-scanner counts ASH reported and fails loudly if the two
disagree. A triage tool that quietly reports "clean" while the scan it is summarizing failed is
worse than no tool, because it is believed.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

# SARIF levels, most severe first. ASH's MEDIUM threshold admits error and warning; note and none
# sit below it and are reported as context only, never as a reason to fail.
ACTIONABLE_LEVELS = ("error", "warning")
LEVEL_SEVERITY = {
    "error": "HIGH/CRITICAL",
    "warning": "MEDIUM",
    "note": "LOW",
    "none": "INFO",
}


class Finding:
    """One unsuppressed SARIF result, flattened to the fields triage actually needs."""

    def __init__(self, result: dict) -> None:
        self.rule = result.get("ruleId") or "(no rule id)"
        self.level = result.get("level") or "none"
        properties = result.get("properties") or {}
        self.scanner = properties.get("scanner_name") or "(unknown scanner)"
        message = result.get("message") or {}
        self.message = (message.get("text") or "").strip()
        self.location = self._location(result)

    @staticmethod
    def _location(result: dict) -> str:
        for location in result.get("locations") or []:
            physical = location.get("physicalLocation") or {}
            artifact = physical.get("artifactLocation") or {}
            uri = artifact.get("uri")
            if not uri:
                continue
            region = physical.get("region") or {}
            line = region.get("startLine")
            # startLine is absent for whole-artifact findings (a dependency CVE is about the
            # manifest, not a line in it), so a bare path is a real answer rather than a gap.
            return f"{uri}:{line}" if line else uri
        return "(no location)"

    @property
    def severity(self) -> str:
        return LEVEL_SEVERITY.get(self.level, self.level.upper())

    def sort_key(self) -> tuple:
        rank = ACTIONABLE_LEVELS.index(self.level) if self.level in ACTIONABLE_LEVELS else 99
        return (rank, self.scanner, self.rule, self.location)


def load_results(output_dir: Path) -> dict:
    aggregated = output_dir / "ash_aggregated_results.json"
    if not aggregated.is_file():
        raise SystemExit(
            f"no ASH results at {aggregated}. Run `make security-scan` first, or pass --output-dir."
        )
    with aggregated.open(encoding="utf-8") as handle:
        return json.load(handle)


def actionable_findings(results: dict) -> list[Finding]:
    findings = []
    for run in (results.get("sarif") or {}).get("runs") or []:
        for result in run.get("results") or []:
            # A suppression entry is ASH applying .ash.yaml. Respecting it here is what keeps this
            # report aligned with the verdict rather than second-guessing a decision already made
            # and justified in config.
            if result.get("suppressions"):
                continue
            if (result.get("level") or "none") not in ACTIONABLE_LEVELS:
                continue
            findings.append(Finding(result))
    findings.sort(key=Finding.sort_key)
    return findings


def reported_actionable_total(results: dict) -> int | None:
    """Sum ASH's own per-scanner actionable counts, or None if it did not report them."""
    scanner_results = results.get("scanner_results")
    if not isinstance(scanner_results, dict):
        return None
    total = 0
    seen = False
    for scanner in scanner_results.values():
        if not isinstance(scanner, dict):
            continue
        count = scanner.get("actionable_finding_count")
        if isinstance(count, int):
            total += count
            seen = True
    return total if seen else None


def render_text(findings: list[Finding]) -> str:
    if not findings:
        return "ASH triage: no actionable findings.\n"
    lines = [f"ASH triage: {len(findings)} actionable finding(s).", ""]
    for finding in findings:
        lines.append(f"  [{finding.severity}] {finding.scanner} :: {finding.rule}")
        lines.append(f"      at {finding.location}")
        if finding.message:
            lines.append(f"      {finding.message}")
        lines.append("")
    return "\n".join(lines)


def render_markdown(findings: list[Finding]) -> str:
    if not findings:
        return "## ASH security scan\n\nNo actionable findings.\n"
    lines = [
        "## ASH security scan",
        "",
        f"**{len(findings)} actionable finding(s)** at or above the configured severity threshold.",
        "",
        "| Severity | Scanner | Rule | Location | Detail |",
        "| --- | --- | --- | --- | --- |",
    ]
    for finding in findings:
        detail = finding.message.replace("|", "\\|")
        if len(detail) > 160:
            detail = detail[:157] + "..."
        lines.append(
            f"| {finding.severity} | {finding.scanner} | `{finding.rule}` "
            f"| `{finding.location}` | {detail} |"
        )
    lines.append("")
    lines.append(
        "Each is unsuppressed. Fix it, or add a `suppressions` entry to `.ash.yaml` with a reason "
        "that says why it is not a risk here."
    )
    lines.append("")
    return "\n".join(lines)


def write_step_summary(markdown: str) -> None:
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if not summary_path:
        return
    with open(summary_path, "a", encoding="utf-8") as handle:
        handle.write(markdown)


def _selftest() -> int:
    """Exercise the paths that only ever run when a scan is already failing.

    This script is a safety tool: on a clean repository every branch below is dead, so a mistake in
    the extraction would sit undetected until the day it is needed and then report nothing. The
    fixtures are inline rather than committed files so there is no second copy of ASH's shape to
    drift out of date.
    """

    def fixture(results, actionable):
        return {
            "scanner_results": {"grype": {"actionable_finding_count": actionable}},
            "sarif": {"runs": [{"results": results}]},
        }

    live = {
        "ruleId": "GO-0000-1-example",
        "level": "error",
        "message": {"text": "A high vulnerability in go-module package: example, version v0.1.0"},
        "properties": {"scanner_name": "grype"},
        "locations": [{"physicalLocation": {"artifactLocation": {"uri": "go.mod"}}}],
    }
    suppressed = dict(live, suppressions=[{"kind": "inSource", "justification": "reviewed"}])
    below = dict(live, level="note")

    failures = []

    found = actionable_findings(fixture([live, suppressed, below], 1))
    if len(found) != 1:
        failures.append(f"expected 1 actionable finding, got {len(found)}")
    elif found[0].location != "go.mod" or found[0].scanner != "grype":
        failures.append(f"finding flattened wrongly: {found[0].location} / {found[0].scanner}")

    if actionable_findings(fixture([suppressed, below], 0)):
        failures.append("a suppressed or below-threshold finding was reported as actionable")

    # The property that makes this script believable: it must not report clean while ASH says two.
    drifted = fixture([suppressed], 2)
    if reported_actionable_total(drifted) != 2 or actionable_findings(drifted):
        failures.append("drift fixture did not set up the disagreement it is meant to prove")

    if not render_text(found).strip() or "GO-0000-1-example" not in render_markdown(found):
        failures.append("a finding was extracted but did not survive rendering")

    for failure in failures:
        sys.stderr.write(f"selftest: {failure}\n")
    if failures:
        return 1
    sys.stdout.write("ASH triage selftest: ok\n")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--selftest",
        action="store_true",
        help="Check the extraction against inline fixtures and exit. Needs no scan output.",
    )
    # No hardcoded default. This path is *read* and then believed, so a predictable location under a
    # world-writable directory is somewhere a local process could plant a report saying everything is
    # fine. The Makefile always passes --output-dir, so the only caller this affects is a bare manual
    # invocation, which is better served by an error than by a guess.
    parser.add_argument(
        "--output-dir",
        default=os.environ.get("ASH_OUTPUT_DIR"),
        help="ASH output directory. Defaults to $ASH_OUTPUT_DIR; required if that is unset.",
    )
    parser.add_argument(
        "--exit-zero",
        action="store_true",
        help="Report findings without failing. For running alongside a scan that already gates.",
    )
    args = parser.parse_args()

    if args.selftest:
        return _selftest()

    if not args.output_dir:
        parser.error("--output-dir is required when ASH_OUTPUT_DIR is not set")

    results = load_results(Path(args.output_dir))
    findings = actionable_findings(results)

    sys.stdout.write(render_text(findings))
    write_step_summary(render_markdown(findings))

    reported = reported_actionable_total(results)
    if reported is not None and reported != len(findings):
        # Never resolved in favour of either number. If this script and ASH disagree about what is
        # actionable, the honest output is that the disagreement exists -- picking one would hide
        # whichever is wrong, and this script exists to be believed.
        sys.stderr.write(
            f"::error::ASH triage disagrees with ASH: this script found {len(findings)} actionable "
            f"finding(s), ASH reported {reported}. The extraction filter and ASH's own verdict have "
            "drifted apart; do not trust either count until they are reconciled.\n"
        )
        return 2

    if findings and not args.exit_zero:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
