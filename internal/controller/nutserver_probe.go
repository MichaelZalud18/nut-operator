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
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

const defaultUpstreamNUTProbeTimeout = 2 * time.Second

type upstreamNUTProbeTarget struct {
	UPSDevice   string
	NUTName     string
	Host        string
	Port        int32
	UPSName     string
	AuthMode    powerv1alpha1.UPSUpstreamNUTAuthMode
	StrictStart bool
}

type upstreamNUTProbeResult struct {
	Reachable bool
	Message   string
}

type upstreamNUTProber interface {
	Probe(ctx context.Context, target upstreamNUTProbeTarget) upstreamNUTProbeResult
}

type tcpUpstreamNUTProber struct {
	Timeout time.Duration
}

func (r *NUTServerReconciler) probeUpstreamNUTDevices(ctx context.Context, devices []powerv1alpha1.UPSDevice) []powerv1alpha1.NUTUpstreamStatus {
	targets := upstreamNUTProbeTargets(devices)
	if len(targets) == 0 {
		return nil
	}

	prober := r.UpstreamProber
	if prober == nil {
		prober = tcpUpstreamNUTProber{Timeout: defaultUpstreamNUTProbeTimeout}
	}

	now := metav1.Now()
	statuses := make([]powerv1alpha1.NUTUpstreamStatus, 0, len(targets))
	for _, target := range targets {
		result := prober.Probe(ctx, target)
		reachable := result.Reachable
		statuses = append(statuses, powerv1alpha1.NUTUpstreamStatus{
			UPSDevice:     target.UPSDevice,
			NUTName:       target.NUTName,
			Host:          target.Host,
			Port:          target.Port,
			UPSName:       target.UPSName,
			AuthMode:      target.AuthMode,
			StrictStart:   target.StrictStart,
			Reachable:     &reachable,
			LastProbeTime: &now,
			Message:       result.Message,
		})
	}
	return statuses
}

func (p tcpUpstreamNUTProber) Probe(ctx context.Context, target upstreamNUTProbeTarget) upstreamNUTProbeResult {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultUpstreamNUTProbeTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	address := net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port)))
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return upstreamNUTProbeResult{
			Reachable: false,
			Message:   fmt.Sprintf("tcp probe failed: %v", err),
		}
	}
	_ = conn.Close()
	return upstreamNUTProbeResult{
		Reachable: true,
		Message:   "tcp probe succeeded",
	}
}

func upstreamNUTProbeTargets(devices []powerv1alpha1.UPSDevice) []upstreamNUTProbeTarget {
	targets := make([]upstreamNUTProbeTarget, 0)
	for _, device := range devices {
		upstream := device.Spec.UpstreamNUT
		if upstream == nil {
			continue
		}
		targets = append(targets, upstreamNUTProbeTarget{
			UPSDevice:   device.Name,
			NUTName:     nutDeviceName(device),
			Host:        upstream.Host,
			Port:        upstreamNUTPort(device),
			UPSName:     upstream.UPSName,
			AuthMode:    upstreamAuthMode(upstream),
			StrictStart: upstreamNUTStrictStart(device),
		})
	}
	return targets
}
