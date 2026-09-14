package controller

import (
	"context"
	"errors"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestProductionWaveTargetsFollowLiveWorkloads(t *testing.T) {
	for _, linear := range []bool{false, true} {
		t.Run(map[bool]string{false: "groups", true: "steps"}[linear], func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := appsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			target := power.ShutdownStepTarget{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shutdown": "yes"}}}
			flow := &power.ShutdownFlow{Spec: power.ShutdownFlowSpec{Groups: []power.ShutdownGroup{{Name: "prepare", Action: power.ShutdownStepWait}, {Name: "scale", Action: power.ShutdownStepScaleWorkload, Target: target}}}, Status: power.ShutdownFlowStatus{CompiledSteps: []power.CompiledShutdownStep{{ID: "prepare"}, {ID: "scale"}}}}
			if linear {
				flow.Spec.Groups = nil
				flow.Spec.Steps = []power.ShutdownStep{{ID: "prepare", Type: power.ShutdownStepWait}, {ID: "scale", Type: power.ShutdownStepScaleWorkload, Target: target}}
			}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"shutdown": "yes"}}}
			old := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "apps"}}
			cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns.DeepCopy(), old.DeepCopy()).Build()
			live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, old).Build()
			r := &ShutdownFlowReconciler{Client: cached, APIReader: live}
			resolve := r.waveTargetResolver(flow, resolver.StructuralBundle{})
			seen := ""
			e := executor.Executor{ResolveTargets: resolve, Runner: guardRefreshRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
				if action.Group.Name == "prepare" {
					if err := live.Delete(ctx, old); err != nil {
						return executor.ActionOutcome{}, err
					}
					if err := live.Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "apps"}}); err != nil {
						return executor.ActionOutcome{}, err
					}
				} else {
					if len(action.Group.SelectedTargets) != 1 {
						return executor.ActionOutcome{}, errors.New("wrong target count")
					}
					seen = action.Group.SelectedTargets[0].Name
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})}
			_, err := e.Execute(context.Background(), executor.Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true, Waves: []executor.Wave{{Groups: []string{"prepare"}}, {Index: 1, Groups: []string{"scale"}}}, Groups: []executor.Group{{Name: "prepare", Action: executor.ActionWait}, {Name: "scale", Action: "ScaleWorkload"}}})
			if err != nil || seen != "new" {
				t.Fatalf("seen=%q err=%v", seen, err)
			}
			ns.Labels = nil
			if err := live.Update(context.Background(), ns); err != nil {
				t.Fatal(err)
			}
			matches, err := resolve(context.Background(), executor.Group{Name: "scale", Action: "ScaleWorkload"})
			if err != nil || len(matches) != 0 {
				t.Fatalf("empty namespace selector broadened: %+v %v", matches, err)
			}
		})
	}
}

func TestWaveNodeMembershipCannotExpandCompiledScope(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, action := range []power.ShutdownStepType{power.ShutdownStepCordonNodes, power.ShutdownStepDrainNodes, power.ShutdownStepAgentShutdown, power.ShutdownStepScaleWorkload} {
		t.Run(string(action), func(t *testing.T) {
			target := power.ShutdownStepTarget{NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"shutdown": "yes"}}}
			if action == power.ShutdownStepAgentShutdown {
				target = power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: "agent"}}}
			}
			flow := &power.ShutdownFlow{Spec: power.ShutdownFlowSpec{Groups: []power.ShutdownGroup{{Name: "act", Action: action, Target: target}}}, Status: power.ShutdownFlowStatus{CompiledSteps: []power.CompiledShutdownStep{{ID: "act"}}}}
			bundle := resolver.StructuralBundle{ClusterNodes: []resolver.ClusterNode{{Name: "allowed", Labels: map[string]string{"shutdown": "yes"}}}, AgentCoverage: []resolver.AgentCoverage{{Name: "agent", Nodes: []string{"allowed"}}}}
			objects := []client.Object{&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "allowed", Labels: map[string]string{"shutdown": "yes"}}}, &power.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: "agent"}, Status: power.NodePowerAgentStatus{SelectedNodes: []string{"allowed"}}}}
			live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			r := &ShutdownFlowReconciler{Client: live, APIReader: live}
			resolve := r.waveTargetResolver(flow, bundle)
			group := executor.Group{Name: "act", Action: string(action)}
			if _, err := resolve(context.Background(), group); err != nil {
				t.Fatal(err)
			}
			if action == power.ShutdownStepAgentShutdown {
				agent := objects[1].(*power.NodePowerAgent)
				agent.Status.SelectedNodes = append(agent.Status.SelectedNodes, "healthy-domain-node")
				if err := live.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
			} else if err := live.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "healthy-domain-node", Labels: map[string]string{"shutdown": "yes"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := resolve(context.Background(), group); err == nil {
				t.Fatal("scope expanded without recompilation")
			}
			if _, err := resolve(context.Background(), executor.Group{Name: "pruned", Action: string(action)}); err == nil {
				t.Fatal("pruned group admitted")
			}
		})
	}
}

func TestWaveTargetReadFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("unavailable")
	}}).Build()
	r := &ShutdownFlowReconciler{Client: c, APIReader: c}
	if _, err := r.scaleWorkloadTargets(context.Background(), power.ShutdownStepTarget{WorkloadSelector: &metav1.LabelSelector{}}); err == nil {
		t.Fatal("failed list accepted")
	}
}

func TestExecutionMetadataDefersInstanceEnumeration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		t.Fatal("instances enumerated before their wave")
		return nil
	}}).Build()
	r := &ShutdownFlowReconciler{Client: c, APIReader: c}
	flow := &power.ShutdownFlow{Spec: power.ShutdownFlowSpec{Groups: []power.ShutdownGroup{{Name: "scale", Action: power.ShutdownStepScaleWorkload, Target: power.ShutdownStepTarget{WorkloadSelector: &metav1.LabelSelector{}}}}}}
	groups, err := r.executorGroups(context.Background(), flow, false)
	if err != nil || len(groups) != 1 || len(groups[0].SelectedTargets) != 0 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
}
