# Governance

NUT Operator is currently a maintainer-led project.

## Decision Model

The maintainer makes final decisions on:

- API compatibility and versioning.
- Security posture and default permissions.
- Release timing and artifact publication.
- Scope boundaries, including supported NUT drivers and node actuation modes.

Design changes should be discussed in public issues or pull requests unless they involve unreleased vulnerability details.

## Project Scope

The project aims to provide Kubernetes-native power management around Network UPS Tools with conservative defaults and scalable controller patterns.

The default support boundary is network-reachable UPS devices. Local USB or serial UPS modes are intentionally out of scope for the default operator path.

## Changes to Governance

Governance may change if the project gains additional maintainers, a formal sponsoring organization, or broader community adoption.
