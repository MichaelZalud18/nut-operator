// Package kube contains explicit-client observation helpers shared by VM guests.
// Provisioning, kubeconfig acquisition, and scenario mutations stay in adapters.
package kube

import (
	"context"
	"fmt"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/readiness"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Client uses only the supplied kubeconfig bytes and caps each API request.
func Client(config []byte, timeout time.Duration) (kubernetes.Interface, error) {
	if len(config) == 0 || timeout <= 0 {
		return nil, fmt.Errorf("explicit kubeconfig and positive API timeout required")
	}
	rest, err := clientcmd.RESTConfigFromKubeConfig(config)
	if err != nil {
		return nil, err
	}
	rest.Timeout = timeout
	return kubernetes.NewForConfig(rest)
}

// CheckIdentity compares against a previously recorded cluster UID. Acquiring
// that trusted UID is the caller's responsibility; this does not infer ownership.
func CheckIdentity(ctx context.Context, client kubernetes.Interface, expected types.UID, timeout time.Duration) error {
	if client == nil || expected == "" || timeout <= 0 {
		return fmt.Errorf("client, expected cluster UID, and positive timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	namespace, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if namespace.UID != expected {
		return fmt.Errorf("cluster identity mismatch")
	}
	return ctx.Err()
}

func NodeReady(node corev1.Node) bool {
	if node.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

type PodIdentity struct {
	Namespace, Name string
	UID             types.UID
	ContainerID     string
	RestartCount    int32
}

// ReadyPod requires an actual ready/running container, not merely PodRunning.
// The comparable identity lets scenarios detect replacement and container restart.
func ReadyPod(pod corev1.Pod, container string) (PodIdentity, error) {
	if container == "" || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return PodIdentity{}, fmt.Errorf("pod is not a live Running instance")
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue
		}
	}
	if ready {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == container && status.Ready && status.State.Running != nil && status.ContainerID != "" {
				return PodIdentity{pod.Namespace, pod.Name, pod.UID, status.ContainerID, status.RestartCount}, nil
			}
		}
	}
	return PodIdentity{}, fmt.Errorf("pod/container is not ready")
}

// WaitForReadyPod observes the caller's namespace-scoped client and selector.
// At most one healthy matching pod is accepted; deleting/unready pods do not count.
func WaitForReadyPod(ctx context.Context, pods typedcorev1.PodInterface, selector, container string, opts readiness.Options) (PodIdentity, error) {
	if pods == nil || selector == "" || container == "" {
		return PodIdentity{}, fmt.Errorf("namespace-scoped pods, selector, and container required")
	}
	var identity PodIdentity
	err := readiness.Wait(ctx, opts, func(ctx context.Context) error {
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		list, err := pods.List(attempt, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return err
		}
		count := 0
		for _, pod := range list.Items {
			if found, err := ReadyPod(pod, container); err == nil {
				identity = found
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("expected exactly one ready pod, got %d", count)
		}
		return nil
	}, nil)
	if err != nil {
		return PodIdentity{}, err
	}
	return identity, nil
}
