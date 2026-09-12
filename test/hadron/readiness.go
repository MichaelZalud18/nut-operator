//go:build hadron

package hadron

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

// Match the actual Ready condition, not the substring also present in NotReady.
func hasReadyNode(output string) bool {
	var nodes corev1.NodeList
	if json.Unmarshal([]byte(output), &nodes) != nil || len(nodes.Items) != 1 {
		return false
	}
	for _, condition := range nodes.Items[0].Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
