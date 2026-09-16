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

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func renderImageReference(image powerv1alpha1.ImageReference, label string) (string, corev1.PullPolicy, error) {
	if image.Repository == "" {
		return "", "", fmt.Errorf("%s rendering requires an image repository", label)
	}

	ref := image.Repository
	if image.Tag != "" {
		ref += ":" + image.Tag
	}
	if image.Digest != "" {
		ref += "@" + image.Digest
	}

	pullPolicy := image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}
	return ref, pullPolicy, nil
}

func durationSeconds(duration *metav1.Duration, fallback int64) int64 {
	if duration == nil {
		return fallback
	}
	seconds := int64(duration.Round(time.Second) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func durationString(duration *metav1.Duration, fallback string) string {
	if duration == nil {
		return fallback
	}
	return duration.Duration.String()
}

// durationOrDefault mirrors durationString for callers that need the value rather than the rendered
// env var, so revocation and the actuator cannot disagree about how long a signal lives.
func durationOrDefault(duration *metav1.Duration, fallback time.Duration) time.Duration {
	if duration == nil || duration.Duration <= 0 {
		return fallback
	}
	return duration.Duration
}

func ptrBool(value bool) *bool {
	return &value
}

func ptrInt32(value int32) *int32 {
	return &value
}

func ptrInt64(value int64) *int64 {
	return &value
}

func ptrProtocol(value corev1.Protocol) *corev1.Protocol {
	return &value
}

func ptrIntOrStringFromInt32(value int32) *intstr.IntOrString {
	port := intstr.FromInt32(value)
	return &port
}

func hashStringMap(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hasher := sha256.New()
	for _, key := range keys {
		hasher.Write([]byte(key))
		hasher.Write([]byte{0})
		hasher.Write([]byte(data[key]))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func hashByteMap(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hasher := sha256.New()
	for _, key := range keys {
		hasher.Write([]byte(key))
		hasher.Write([]byte{0})
		hasher.Write(data[key])
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
