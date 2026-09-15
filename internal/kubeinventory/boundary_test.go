package kubeinventory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type interceptReader struct {
	client.Reader
	list func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r interceptReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.list(ctx, list, opts...)
}

func TestStructuralReadFailuresNeverProducePartialBundle(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(clusterContextScheme(t)).Build()
	for _, failed := range []client.ObjectList{
		&power.UPSDeviceList{}, &power.PowerInfrastructureList{}, &power.PowerInventoryNodeList{},
		&power.PowerInventoryEdgeList{}, &corev1.NodeList{}, &power.NodePowerAgentList{}, &power.UPSCapabilityProfileList{},
	} {
		for _, cause := range []error{errors.New("reader unavailable"), context.Canceled, context.DeadlineExceeded} {
			t.Run(fmt.Sprintf("%T/%s", failed, cause), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				reader := interceptReader{Reader: base, list: func(got context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if got != ctx {
						t.Fatal("caller context was replaced")
					}
					if reflect.TypeOf(list) == reflect.TypeOf(failed) {
						return cause
					}
					return base.List(got, list, opts...)
				}}
				bundle, _, err := ResolveStructuralBundle(ctx, reader)
				if !errors.Is(err, cause) || !reflect.DeepEqual(bundle, resolver.StructuralBundle{}) {
					t.Fatalf("bundle=%+v error=%v; want empty bundle and wrapped %v", bundle, err, cause)
				}
			})
		}
	}
}

func TestInvalidInventoryPreservesValidationDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		object                  client.Object
		reason, source, subject string
	}{
		{&power.UPSDevice{ObjectMeta: objectMeta("bad")}, "DriverRequired", resolver.DiagnosticSourceInventory, "UPSDevice/bad"},
		{&power.PowerInventoryNode{ObjectMeta: objectMeta("bad")}, "NodeNameRequired", resolver.DiagnosticSourceInventory, "PowerInventoryNode/bad"},
		{&power.PowerInventoryEdge{ObjectMeta: objectMeta("bad")}, "FromRequired", resolver.DiagnosticSourceInventory, "PowerInventoryEdge/bad"},
		{&power.UPSCapabilityProfile{ObjectMeta: objectMeta("bad")}, "ProfileVersionRequired", resolver.DiagnosticSourceCapability, "UPSCapabilityProfile/bad"},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			reader := fake.NewClientBuilder().WithScheme(clusterContextScheme(t)).WithObjects(tc.object).Build()
			bundle, diagnostics, err := ResolveStructuralBundle(context.Background(), reader)
			if !errors.Is(err, resolver.ErrRejected) || bundle.Hash != "" || len(diagnostics) != 1 {
				t.Fatalf("bundle=%+v diagnostics=%+v error=%v", bundle, diagnostics, err)
			}
			d := diagnostics[0]
			if d.Reason != tc.reason || d.Source != tc.source || d.Subject != tc.subject || d.Severity != resolver.DiagnosticError || d.Message == "" {
				t.Fatalf("validation diagnostic changed: %+v", d)
			}
		})
	}
}

func TestStructuralBundlePreservesTopologyAcrossListOrder(t *testing.T) {
	objects := []client.Object{
		&power.UPSDevice{ObjectMeta: objectMeta("ups"), Spec: power.UPSDeviceSpec{Driver: "dummy-ups", PowerDomains: []string{"rack"}}},
		&power.PowerInfrastructure{ObjectMeta: objectMeta("switch")},
		&power.PowerInventoryNode{ObjectMeta: objectMeta("inventory-record"), Spec: power.PowerInventoryNodeSpec{
			NodeName: "actual-node", Roles: power.PowerInventoryNodeRoles{
				LastDitchRole: "control-plane", ControlPlane: ptr.To(true), ControlPlaneQuorumMember: ptr.To(true),
			},
		}},
		&corev1.Node{ObjectMeta: objectMeta("actual-node")},
	}
	for i, edge := range []power.PowerInventoryEdgeSpec{
		{From: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityUPSDevice, Name: "ups"}, To: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityNode, Name: "actual-node"}, Relation: power.PowerInventoryEdgeFeeds, Input: "psu"},
		{From: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityUPSDevice, Name: "ups"}, To: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityPowerInfrastructure, Name: "switch"}, Relation: power.PowerInventoryEdgeFeeds, Input: "psu"},
		{From: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityPowerInfrastructure, Name: "switch"}, To: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityNode, Name: "actual-node"}, Relation: power.PowerInventoryEdgeCarries},
	} {
		objects = append(objects, &power.PowerInventoryEdge{ObjectMeta: objectMeta(fmt.Sprintf("edge-%d", i)), Spec: edge})
	}
	base := fake.NewClientBuilder().WithScheme(clusterContextScheme(t)).WithObjects(objects...).Build()
	reversed := interceptReader{Reader: base, list: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
		if err := base.List(ctx, list, opts...); err != nil {
			return err
		}
		switch list := list.(type) {
		case *power.PowerInventoryEdgeList:
			slices.Reverse(list.Items)
		case *power.UPSDeviceList:
			slices.Reverse(list.Items)
		case *power.PowerInventoryNodeList:
			slices.Reverse(list.Items)
		}
		return nil
	}}
	want, diagnostics, err := ResolveStructuralBundle(context.Background(), base)
	if err != nil {
		t.Fatalf("resolve: %v (%+v)", err, diagnostics)
	}
	got, gotDiagnostics, err := ResolveStructuralBundle(context.Background(), reversed)
	if err != nil || !reflect.DeepEqual(want, got) || !reflect.DeepEqual(diagnostics, gotDiagnostics) || got.Hash == "" {
		t.Fatalf("list order changed bundle or diagnostics: %v\nwant=%+v\ngot=%+v", err, want, got)
	}
	for _, entity := range got.Topology.Entities {
		if entity.Kind == inventory.EntityKindNode && (entity.ID != "actual-node" || !entity.ControlPlane || !entity.ControlPlaneQuorumMember || entity.ShutdownTier == nil || *entity.ShutdownTier != 1 || entity.LastDitchRole != "control-plane") {
			t.Fatalf("node identity or roles lost: %+v", entity)
		}
	}
	if len(got.Topology.Domains) != 1 || !slices.Equal(got.Topology.Domains[0].Infrastructure, []string{"switch"}) || !slices.Equal(got.Topology.Domains[0].Nodes, []string{"actual-node"}) {
		t.Fatalf("power domain lost: %+v", got.Topology.Domains)
	}
	orders := got.Topology.CommunicationOrders
	if len(orders) != 1 || orders[0].From != "actual-node" || orders[0].To != "switch" || !strings.Contains(orders[0].Source, "edge-2") {
		t.Fatalf("communication order/provenance lost: %+v", orders)
	}
}

func TestDeviceCapabilityLookupSkipsInvalidProfileAndPreservesReadError(t *testing.T) {
	base := fake.NewClientBuilder().WithScheme(clusterContextScheme(t)).WithObjects(&power.UPSCapabilityProfile{ObjectMeta: objectMeta("invalid")}).Build()
	device := &power.UPSDevice{ObjectMeta: objectMeta("ups"), Spec: power.UPSDeviceSpec{Driver: "dummy-ups", Identity: power.UPSDeviceIdentitySpec{Model: "TOWER_1000VA_230V"}}}
	match, _, err := ResolveDeviceCapabilityMatch(context.Background(), base, device)
	if err != nil || match.ProfileID != capability.BundledUbiquitiUPSTowerProfileID {
		t.Fatalf("invalid CR profile blocked bundled matching: %+v, %v", match, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := interceptReader{Reader: base, list: func(got context.Context, _ client.ObjectList, _ ...client.ListOption) error { return got.Err() }}
	match, _, err = ResolveDeviceCapabilityMatch(ctx, reader, device)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(match, capability.MatchResult{}) {
		t.Fatalf("canceled lookup returned match=%+v error=%v", match, err)
	}
}
