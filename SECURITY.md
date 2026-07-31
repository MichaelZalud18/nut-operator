# Security Policy

## Supported Versions

This project is pre-1.0 and should be treated as alpha software. Security fixes are provided on the default branch until the project begins tagged release maintenance.

## Reporting a Vulnerability

Do not report suspected vulnerabilities with sensitive details in public issues.

Preferred reporting path:

1. Use GitHub private vulnerability reporting for this repository when available.
2. If private reporting is unavailable, open a public issue with only a minimal request for a security contact. Do not include exploit details, credentials, private infrastructure data, or logs containing secrets.

Reports should include:

- The affected component or API.
- The impact and expected attack path.
- A minimal reproduction that avoids private credentials and private infrastructure details.
- Any known mitigations.

## Security Scope

Security-sensitive areas include:

- Controller RBAC and Kubernetes API access.
- Operand image provenance, SBOMs, and signing.
- NUT credentials and generated `upsd`/`upsmon` configuration.
- Node shutdown authorization and actuator isolation.
- PostgreSQL/CNPG audit storage and retention.

For architecture-level controls, see [docs/security.md](docs/security.md).
