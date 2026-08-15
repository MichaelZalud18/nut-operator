#!/usr/bin/env python3
"""Validate every sample and example manifest against the generated CRD schemas.

`config/samples/` is applied verbatim -- `kubectl apply -k config/samples/` is in the README -- and
`docs/examples/` is what people copy. Both were unchecked, so a sample could reference a field shape
the CRD had never accepted and nothing would say so until a user hit the API server's rejection.

That is not hypothetical: `power_v1alpha1_upscapabilityprofile.yaml` shipped `quirks` as a list of
bare strings after `OD-22`/`F-26` made quirks structured objects. The sample had been wrong since
that change and no gate noticed, because the CRDs are generated from Go types and the samples are
hand-written YAML -- nothing connected the two.

This connects them. It is deliberately schema-only: it checks what the API server's structural
schema would reject, not what the webhooks would. Defaulting and cross-field validation live in
admission and are covered by envtest; duplicating them here would mean maintaining a second
implementation of rules that already have one.
"""

from __future__ import annotations

import glob
import sys

import yaml

try:
    import jsonschema
except ImportError:  # pragma: no cover - environment guard
    sys.stderr.write(
        "jsonschema is not installed. Install it (pip install jsonschema pyyaml) or run this "
        "through `make validate-samples`, which reports the same thing.\n"
    )
    raise SystemExit(2)

CRD_GLOB = "config/crd/bases/*.yaml"
TARGET_GLOBS = ("config/samples/*.yaml", "docs/examples/**/*.yaml")
API_GROUP = "power.zalud.io/"


def load_schemas() -> dict[tuple[str, str], dict]:
    schemas: dict[tuple[str, str], dict] = {}
    for path in sorted(glob.glob(CRD_GLOB)):
        crd = yaml.safe_load(open(path, encoding="utf-8"))
        kind = crd["spec"]["names"]["kind"]
        for version in crd["spec"]["versions"]:
            schemas[(kind, version["name"])] = version["schema"]["openAPIV3Schema"]
    return schemas


def strip_extensions(node):
    """Drop the x-kubernetes-* keywords, which jsonschema does not understand.

    They constrain merge and list semantics rather than document shape, so removing them costs no
    structural coverage.
    """
    if isinstance(node, dict):
        return {k: strip_extensions(v) for k, v in node.items() if not k.startswith("x-kubernetes-")}
    if isinstance(node, list):
        return [strip_extensions(v) for v in node]
    return node


def targets() -> list[str]:
    found: list[str] = []
    for pattern in TARGET_GLOBS:
        found.extend(glob.glob(pattern, recursive=True))
    return sorted(set(found))


def main() -> int:
    schemas = load_schemas()
    if not schemas:
        sys.stderr.write(f"no CRDs found at {CRD_GLOB}; run `make manifests` first\n")
        return 2

    problems = 0
    checked = 0
    for path in targets():
        for doc in yaml.safe_load_all(open(path, encoding="utf-8")):
            if not doc or "kind" not in doc:
                continue
            api_version = str(doc.get("apiVersion", ""))
            if not api_version.startswith(API_GROUP):
                continue
            key = (doc["kind"], api_version.split("/", 1)[1])
            name = doc.get("metadata", {}).get("name", "(unnamed)")
            if key not in schemas:
                print(f"  MISSING CRD  {path}: no schema for {doc['kind']} {api_version}")
                problems += 1
                continue
            checked += 1
            validator = jsonschema.Draft7Validator(strip_extensions(schemas[key]))
            for error in sorted(validator.iter_errors(doc), key=lambda e: list(e.path)):
                field = ".".join(str(p) for p in error.path) or "(root)"
                print(f"  INVALID  {path} [{doc['kind']}/{name}] {field}: {error.message}")
                problems += 1

    if problems:
        print(f"\n{problems} problem(s) across {checked} manifest(s).")
        print("The CRDs are generated from the Go types, so the manifest is what to fix.")
        return 1
    print(f"All {checked} sample and example manifests validate against the CRD schemas.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
