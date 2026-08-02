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

package telemetry

import "github.com/MichaelZalud18/nut-operator/internal/audit"

// AuditSnapshot converts normalized telemetry into the durable audit shape.
func AuditSnapshot(snapshotID string, snapshot Snapshot) audit.TelemetrySnapshot {
	return audit.TelemetrySnapshot{
		SnapshotID:           snapshotID,
		ObservedAt:           snapshot.ObservedAt,
		UPSDevice:            snapshot.UPSDevice,
		NUTServer:            snapshot.NUTServer,
		NUTName:              snapshot.NUTName,
		UPSStatus:            snapshot.UPSStatus,
		BatteryChargePercent: snapshot.BatteryChargePercent,
		RuntimeSeconds:       snapshot.RuntimeSeconds,
		LoadPercent:          snapshot.LoadPercent,
		Variables:            copyVariables(snapshot.Variables),
	}
}
