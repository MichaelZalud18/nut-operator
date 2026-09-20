package image

import (
	"strings"
	"testing"
	"time"
)

func TestReferenceBoundaries(t *testing.T) {
	for _, value := range []string{"docker.io/library/operand:run-123", "localhost:5000/manager:run-123"} {
		repo, tag, err := SplitTagged(value)
		if err != nil || repo+":"+tag != value {
			t.Fatalf("tag split: %q %q %v", repo, tag, err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	ref, err := Parse("registry:5000/operand@" + digest)
	if err != nil || ref.Repository != "registry:5000/operand" || ref.Digest != digest || ref.Tag != "" {
		t.Fatalf("digest parsing: %+v, %v", ref, err)
	}
	if _, _, err := SplitTagged("registry:5000/operand@" + digest); err == nil {
		t.Fatal("digest passed to tag-only consumer")
	}
	for _, invalid := range []string{"", "registry:5000/operand", "image:", ":tag", " image:tag", "https://image:tag", "image@sha256:bad", "image:bad/tag"} {
		if _, err := Parse(invalid); err == nil {
			t.Fatalf("invalid/unpinned reference accepted: %q", invalid)
		}
	}
}

func TestBuildCommandsPreserveGuestDeliveryBoundary(t *testing.T) {
	for _, ref := range []string{"docker.io/library/hadron-test:123", "localhost:5000/talos-test:123"} {
		operand, err := OperandBuild("/workspace", "images/actuator/Dockerfile", ref, time.Minute, nil)
		if err != nil || operand.Name != "docker" || strings.Join(operand.Args, " ") != "build -f /workspace/images/actuator/Dockerfile -t "+ref+" /workspace" {
			t.Fatalf("operand build: %+v %v", operand, err)
		}
		manager, err := ManagerBuild("/workspace", ref, time.Minute, nil)
		if err != nil || manager.Name != "make" || strings.Join(manager.Args, " ") != "docker-build IMG="+ref {
			t.Fatalf("manager provenance target lost: %+v %v", manager, err)
		}
	}
	if _, err := OperandBuild("/workspace", "../elsewhere/Dockerfile", "image:tag", time.Minute, nil); err == nil {
		t.Fatal("Dockerfile escaped workspace")
	}
}
