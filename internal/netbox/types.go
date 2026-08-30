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

package netbox

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Source is a read-only NetBox DCIM snapshot.
type Source struct {
	ObservedAt time.Time
	Devices    []Device
	PowerPorts []PowerPort
	Interfaces []Interface
}

// Device is the subset of NetBox dcim.Device the mapper needs.
type Device struct {
	ID           int                        `json:"id"`
	Name         string                     `json:"name"`
	Display      string                     `json:"display"`
	DisplayName  string                     `json:"display_name"`
	Description  string                     `json:"description"`
	Role         *BriefObject               `json:"role"`
	DeviceType   *DeviceType                `json:"device_type"`
	CustomFields map[string]json.RawMessage `json:"custom_fields"`
}

// DeviceType is the subset of NetBox dcim.DeviceType used for UPS model hints.
type DeviceType struct {
	Model        string       `json:"model"`
	Manufacturer *BriefObject `json:"manufacturer"`
}

// PowerPort is the subset of NetBox dcim.PowerPort used for feeds edges.
type PowerPort struct {
	ID                         int         `json:"id"`
	Name                       string      `json:"name"`
	Label                      string      `json:"label"`
	Device                     BriefObject `json:"device"`
	ConnectedEndpoints         []Endpoint  `json:"connected_endpoints"`
	ConnectedEndpoint          *Endpoint   `json:"connected_endpoint"`
	ConnectedEndpointsType     string      `json:"connected_endpoints_type"`
	ConnectedEndpointType      string      `json:"connected_endpoint_type"`
	ConnectedEndpointReachable bool        `json:"connected_endpoint_reachable"`
}

// Interface is the subset of NetBox dcim.Interface used for carries edges.
type Interface struct {
	ID                     int         `json:"id"`
	Name                   string      `json:"name"`
	Device                 BriefObject `json:"device"`
	ConnectedEndpoints     []Endpoint  `json:"connected_endpoints"`
	ConnectedEndpoint      *Endpoint   `json:"connected_endpoint"`
	ConnectedEndpointsType string      `json:"connected_endpoints_type"`
	ConnectedEndpointType  string      `json:"connected_endpoint_type"`
}

// Endpoint is a cabled far-end termination as returned on NetBox component
// serializers. Device-backed terminations are sufficient for node/UPS/PDU
// topology; power-panel-backed feeds are materialized as infrastructure.
type Endpoint struct {
	ID         int          `json:"id"`
	URL        string       `json:"url"`
	Name       string       `json:"name"`
	Display    string       `json:"display"`
	ObjectType string       `json:"object_type"`
	Device     *BriefObject `json:"device"`
	PowerPanel *BriefObject `json:"power_panel"`
}

// BriefObject is NetBox's compact related-object shape.
type BriefObject struct {
	ID          int    `json:"id"`
	URL         string `json:"url"`
	Name        string `json:"name"`
	Display     string `json:"display"`
	DisplayName string `json:"display_name"`
	Slug        string `json:"slug"`
}

func (b BriefObject) text() string {
	for _, value := range []string{b.Name, b.Display, b.DisplayName, b.Slug} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if b.ID > 0 {
		return strconv.Itoa(b.ID)
	}
	return ""
}

func (d Device) display() string {
	for _, value := range []string{d.Display, d.DisplayName, d.Name} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if d.ID > 0 {
		return fmt.Sprintf("NetBox device %d", d.ID)
	}
	return "NetBox device"
}

func componentEndpoints(many []Endpoint, one *Endpoint) []Endpoint {
	if len(many) > 0 {
		return many
	}
	if one != nil {
		return []Endpoint{*one}
	}
	return nil
}
