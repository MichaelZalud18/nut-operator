package v1alpha1

import (
	"context"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

func TestUnsupportedFailurePoliciesAreRejected(t *testing.T) {
	for _, policy := range []string{"continue", "notify", "step"} {
		t.Run(policy, func(t *testing.T) {
			flow := &power.ShutdownFlow{Spec: validShutdownFlowSpec()}
			flow.Name = "test"
			switch policy {
			case "continue":
				flow.Spec.AbortPolicy.Behavior = power.AbortBehaviorContinueSafeSteps
			case "notify":
				flow.Spec.AbortPolicy.Notify = ptrBool(true)
			case "step":
				flow.Spec.Groups = nil
				flow.Spec.Steps = []power.ShutdownStep{{ID: "notify", Type: power.ShutdownStepNotify, ContinueOnError: ptrBool(true)}}
			}
			validator := &ShutdownFlowCustomValidator{}
			if _, err := validator.ValidateCreate(context.Background(), flow); err == nil {
				t.Fatal("unsupported create accepted")
			}
			if _, err := validator.ValidateUpdate(context.Background(), &power.ShutdownFlow{Spec: validShutdownFlowSpec()}, flow); err == nil {
				t.Fatal("unsupported update accepted")
			}
			inputs, err := shutdownflow.PlannerInputs(flow)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := planner.Compile(inputs, planner.TelemetryInputs{}); err == nil {
				t.Fatal("legacy object bypassed planner validation")
			}
		})
	}
}

func TestFailurePolicyDefaultsMatchExecution(t *testing.T) {
	flow := &power.ShutdownFlow{Spec: validShutdownFlowSpec()}
	flow.Spec.AbortPolicy = power.AbortPolicySpec{}
	defaultShutdownFlow(flow)
	if flow.Spec.AbortPolicy.Notify == nil || *flow.Spec.AbortPolicy.Notify {
		t.Fatal("default promises unsupported abort notifications")
	}
	if _, err := (&ShutdownFlowCustomValidator{}).ValidateCreate(context.Background(), flow); err != nil {
		t.Fatal(err)
	}
}
