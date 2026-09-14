package resolver

import (
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

func TestInventoryControlPlaneRolesReachPlanner(t *testing.T) {
	bundle := StructuralBundle{Topology: inventory.Topology{Entities: []inventory.Entity{
		{ID: "api", Kind: inventory.EntityKindNode, ControlPlane: true},
		{ID: "voter", Kind: inventory.EntityKindNode, ControlPlaneQuorumMember: true},
		{ID: "worker", Kind: inventory.EntityKindNode},
	}}}
	input := AttachResolvedInputHash(planner.StructuralInputs{}, bundle)
	if len(input.ControlPlaneNodes) != 2 || input.ControlPlaneNodes[0].Name != "api" || input.ControlPlaneNodes[0].QuorumMember || !input.ControlPlaneNodes[1].QuorumMember {
		t.Fatalf("roles=%+v", input.ControlPlaneNodes)
	}
}
