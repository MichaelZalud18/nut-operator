#!/usr/bin/env python3
"""Bind the example to three actual Node names; emit topology YAML without cluster access."""

import argparse
from pathlib import Path
import re
import sys

import yaml


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for role in ("control", "worker-a", "worker-b"):
        parser.add_argument("--" + role, required=True)
    args = parser.parse_args()
    names = [args.control, args.worker_a, args.worker_b]
    label = r"[a-z0-9](?:[-a-z0-9]*[a-z0-9])?"
    subdomain = rf"{label}(?:\.{label})*"
    if len(set(names)) != 3 or any(
        len(name) > 253 or not re.fullmatch(subdomain, name)
        for name in names
    ):
        parser.error("supply three distinct Kubernetes Node names from kubectl get nodes")
    bindings = dict(zip(("QUICKSTART_CONTROL", "QUICKSTART_WORKER_A", "QUICKSTART_WORKER_B"), names))
    with Path(__file__).with_name("topology.yaml").open(encoding="utf-8") as source:
        documents = list(yaml.safe_load_all(source))
    for document in documents:
        spec = document["spec"]
        if document["kind"] == "PowerInventoryNode":
            spec["nodeName"] = bindings[spec["nodeName"]]
        elif document["kind"] == "PowerInventoryEdge" and spec["to"]["kind"] == "Node":
            spec["to"]["name"] = bindings[spec["to"]["name"]]
    yaml.safe_dump_all(documents, sys.stdout, sort_keys=False)


if __name__ == "__main__":
    main()
