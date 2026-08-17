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

package haltwatch

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Reporting says whether a node currently reports itself Ready.
//
// A node with no Ready condition at all counts as not reporting. That is a node the control plane
// has never heard from, which for this purpose is the same as one it has stopped hearing from.
func Reporting(node *corev1.Node) bool {
	condition := readyCondition(node)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// LastAliveAt estimates when a node that is no longer Ready was last actually running.
//
// The obvious answer -- the Ready condition's LastTransitionTime -- is the wrong end of the
// interval. The transition is written by the control plane's node lifecycle controller once a
// kubelet has been silent for the node-monitor grace period, so it lands a fixed lag after the
// machine actually stopped, and using it inflates every halt measurement by that lag.
//
// LastHeartbeatTime is the last moment kubelet is known to have posted status, which is much closer
// to when the machine stopped. The two are taken together rather than trusting either alone: the
// earlier of the two is used, so the result is the tightest upper bound available on the moment the
// node went away. If the heartbeat is preserved across the transition, that is the tight answer; if
// the transition rewrites it, the earlier value is the transition itself and nothing is lost. Either
// way the estimate never moves later than the pessimistic reading.
//
// What remains is a bias low, bounded by kubelet's own status-post interval: a node can keep running
// for up to one interval after its last post. That direction is the right one to err in for the
// measurement's purpose -- it is evidence for a runtime reserve, and a reserve sized from an
// optimistic tail is only wrong in the direction of being generous.
//
// The zero time is returned when the node reports no Ready condition at all, leaving the caller to
// supply its own observation time.
func LastAliveAt(node *corev1.Node) time.Time {
	condition := readyCondition(node)
	if condition == nil {
		return time.Time{}
	}
	heartbeat := condition.LastHeartbeatTime.Time
	transition := condition.LastTransitionTime.Time
	switch {
	case heartbeat.IsZero():
		return transition
	case transition.IsZero():
		return heartbeat
	case heartbeat.Before(transition):
		return heartbeat
	default:
		return transition
	}
}

func readyCondition(node *corev1.Node) *corev1.NodeCondition {
	if node == nil {
		return nil
	}
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}
