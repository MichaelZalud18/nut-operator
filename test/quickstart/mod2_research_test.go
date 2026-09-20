package quickstart_test

import (
	"context"
	"os"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeinventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	webhooks "github.com/MichaelZalud18/nut-operator/internal/webhook/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMOD2MixedHookAndAgentPlan(t *testing.T) {
	scheme, objects, flow := fixture(t)
	data, err := os.ReadFile("../../docs/examples/mixed-hooks/hook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(data, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook := obj.(*power.ShutdownHook)
	if err := (&webhooks.ShutdownHookCustomDefaulter{}).Default(context.Background(), hook); err != nil {
		t.Fatal(err)
	}
	if _, err := (&webhooks.ShutdownHookCustomValidator{}).ValidateCreate(context.Background(), hook); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, hook)
	flow.Spec.Groups = append([]power.ShutdownGroup{{Name: hook.Name, Action: "RunHook", HookRef: &power.NamespacedNameReference{Namespace: hook.Namespace, Name: hook.Name}, ShutdownTier: ptr.To(int32(3)), Before: []string{"drain-workers"}, Timeout: &metav1.Duration{Duration: 10 * time.Second}}}, flow.Spec.Groups...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	bundle, diagnostics, err := kubeinventory.ResolveStructuralBundle(context.Background(), c)
	if err != nil {
		t.Fatalf("resolve: %v %+v", err, diagnostics)
	}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	for _, d := range compiled.Diagnostics {
		if d.Severity == planner.DiagnosticError {
			t.Fatalf("compile: %+v", compiled.Diagnostics)
		}
	}
	if len(compiled.Waves) != 4 || len(compiled.Waves[0].Groups) != 1 || compiled.Waves[0].Groups[0] != hook.Name {
		t.Fatalf("unexpected ordering: %+v", compiled.Waves)
	}
	for _, membership := range shutdownflow.PlannerGroupNodes(flow, bundle) {
		if membership.Group == hook.Name {
			t.Fatalf("static external host incorrectly modeled as Kubernetes membership: %+v", membership)
		}
	}
	if len(compiled.BlockedNodeReleases) != 0 {
		t.Fatalf("built-in releases blocked: %+v", compiled.BlockedNodeReleases)
	}
}
