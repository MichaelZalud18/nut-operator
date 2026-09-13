// Package nutsupervisor owns the NUT driver lifecycle independently of Kubernetes rendering.
package nutsupervisor

import _ "embed"

//go:embed supervisor.sh
var script string

// Script returns the exact source shipped in the manager and executed by the operand.
func Script() string { return script }
