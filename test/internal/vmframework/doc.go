// Package vmframework documents the composition of shared VM test modules.
// Artifact preparation and readiness checks have no guest-specific dependencies.
// Hadron and Talos supply provisioning and readiness callbacks; vmprocess owns
// verified process cleanup. Scenario assertions remain in their owning tests.
package vmframework
