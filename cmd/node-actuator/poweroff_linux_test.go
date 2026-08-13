//go:build linux

package main

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// permittedHasSysBoot reports whether this test process could raise CAP_SYS_BOOT at all.
//
// The test has to branch on it rather than assume: run as an ordinary user the capability is
// absent, and run as root in a CI container it is present. Asserting one outcome unconditionally
// would pass in one environment and fail in the other for reasons that have nothing to do with the
// code.
func permittedHasSysBoot(t *testing.T) bool {
	t.Helper()

	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		t.Fatalf("read capabilities: %v", err)
	}
	index := capSysBoot / 32
	bit := uint32(1) << (capSysBoot % 32)
	return data[index].Permitted&bit != 0
}

// The startup check is the only thing standing between a silently inert actuator and a visible
// failure, so its message has to be able to send someone to the right place. There are exactly
// three ways to arrive here and they need completely different fixes -- fix the CR, fix the image
// pipeline, or fix how the container invokes the binary -- and none of them is guessable from
// "operation not permitted".
func TestVerifySysBootNamesEveryWayTheCapabilityCanBeLost(t *testing.T) {
	if permittedHasSysBoot(t) {
		t.Skip("CAP_SYS_BOOT is held here, so the failure message is not produced")
	}

	err := verifySysBootAvailable()
	if err == nil {
		t.Fatal("verifySysBootAvailable must fail when CAP_SYS_BOOT is not permitted")
	}
	for _, cause := range []string{"securityContext", "security.capability", "entrypoint"} {
		if !strings.Contains(err.Error(), cause) {
			t.Errorf("the message must point at %q as a possible cause, got: %v", cause, err)
		}
	}
}

// F-61 re-verification: the file capability survives only when the container runtime execs the
// binary. Measured on kind, with one image, one pod, and one securityContext:
//
//	runtime execs /usr/local/bin/probe   -> CapPrm 0000000000400000
//	sh -c '/usr/local/bin/probe'         -> CapPrm 0000000000000000
//
// The kernel's downgrade branch in cap_bprm_creds_from_file intersects the new permitted set with
// the caller's whenever no_new_privs is set and execve would gain a capability. For the entrypoint
// the caller is the runtime's init, which already holds CAP_SYS_BOOT from the OCI spec, so nothing
// is gained and nothing is taken away. For anything spawned inside the container the caller is a
// capability-dumb process whose permitted set is empty, so the gain is real and the capability is
// stripped.
//
// Wrapping the entrypoint in a shell would therefore disarm the actuator while leaving every test,
// probe, and status field looking healthy.
func TestDockerfileExecsTheActuatorAsTheEntrypoint(t *testing.T) {
	dockerfile, err := os.ReadFile("../../images/node-actuator/Dockerfile")
	if err != nil {
		t.Fatalf("read node-actuator Dockerfile: %v", err)
	}

	if !strings.Contains(string(dockerfile), `ENTRYPOINT ["/node-actuator"]`) {
		t.Error("the actuator must be the container entrypoint in exec form; a shell wrapper silently drops cap_sys_boot (F-61)")
	}
}

// F-61: without CAP_SYS_BOOT in the permitted set, reboot(2) returns EPERM. The actuator must say
// so plainly rather than letting the syscall fail with a bare errno at the one moment a node was
// supposed to power off.
func TestRaiseSysBootFailsClosedWithoutThePermittedCapability(t *testing.T) {
	err := raiseSysBoot()

	if permittedHasSysBoot(t) {
		if err != nil {
			t.Fatalf("CAP_SYS_BOOT is permitted here, so raising it must succeed: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("raising CAP_SYS_BOOT must fail when it is not in the permitted set")
	}
	if !strings.Contains(err.Error(), "permitted set") {
		t.Fatalf("the error must name the actual problem, got: %v", err)
	}
}

// The syscall must never be reached without the capability. Ordering matters here in a way it
// rarely does: reboot(2) with an empty effective set fails with EPERM, which reads in the log as an
// unexplained refusal rather than as a configuration mistake.
func TestPoweroffRaisesBeforeCallingReboot(t *testing.T) {
	if permittedHasSysBoot(t) {
		t.Skip("this process could actually halt the machine; the ordering assertion runs unprivileged only")
	}

	if err := rebootPoweroff(); err == nil {
		t.Fatal("poweroff must refuse before calling reboot(2) when CAP_SYS_BOOT is unavailable")
	} else if !strings.Contains(err.Error(), "permitted set") {
		t.Fatalf("poweroff must fail on the capability check rather than inside the syscall, got: %v", err)
	}
}

// The file capability is what puts CAP_SYS_BOOT in the permitted set, and it is applied to the
// binary at image build time. This asserts the Dockerfile keeps doing that, and keeps doing it as
// permitted-only: cap_sys_boot=ep makes execve fail with EPERM wherever the capability is outside
// the bounding set, which is every deployment running the DryRun/Stub default.
func TestDockerfileAppliesPermittedOnlyFileCapability(t *testing.T) {
	dockerfile, err := os.ReadFile("../../images/node-actuator/Dockerfile")
	if err != nil {
		t.Fatalf("read node-actuator Dockerfile: %v", err)
	}
	content := string(dockerfile)

	if !strings.Contains(content, "setcap cap_sys_boot=p ") {
		t.Error("the actuator binary must carry cap_sys_boot=p, or reboot(2) fails with EPERM (F-61)")
	}
	if strings.Contains(content, "setcap cap_sys_boot=ep ") {
		t.Error("cap_sys_boot=ep makes execve fail wherever the capability is outside the bounding set")
	}
}
