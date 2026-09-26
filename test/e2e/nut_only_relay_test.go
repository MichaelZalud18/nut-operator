//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func nutOnlyRelayAcceptance(source power.NUTServer, fingerprint string) {
	device := nutOnlyDevice("mod4-relayed")
	device.Spec.Driver = ""
	device.Spec.UpstreamNUT = &power.UPSUpstreamNUTSpec{Host: "mod4." + nutOnlyNS + ".svc", UPSName: "mod4-ups", StrictStart: ptr.To(true)}
	for _, mode := range []power.UPSUpstreamNUTAuthMode{power.UPSUpstreamNUTAuthDefault, power.UPSUpstreamNUTAuthSecret} {
		denied := device.DeepCopy()
		denied.Spec.UpstreamNUT.Auth.Mode = mode
		if mode == power.UPSUpstreamNUTAuthSecret {
			denied.Spec.UpstreamNUT.Auth.SecretKeyRef = &power.SecretKeyReference{Namespace: nutOnlyNS, Name: "not-read", Key: "nutauth.conf"}
		}
		cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(logicalFlowJSONList(denied))
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("does not support upstream authconf"))
	}
	relay := nutOnlyServer("mod4-relay", device.Name)
	relay.Spec.Auth = source.Spec.Auth
	Expect(applyFixtureManifest(logicalFlowJSONList(device, relay))).To(Succeed())
	nutOnlyReady(relay.Name)
	nutOnlyReadEventually(relay.Name, device.Name, "ups.model", "updated-model", fingerprint)
	updated := nutOnlyDevice("mod4-ups")
	updated.Spec.DisplayName = "relayed-update"
	Expect(applyFixtureManifest(logicalFlowJSONList(updated))).To(Succeed())
	nutOnlyReadEventually(relay.Name, device.Name, "ups.model", "relayed-update", fingerprint)
	var driverConfig corev1.Secret
	Expect(logicalFlowGet(&driverConfig, "secret", "mod4-relay-nut-driver-config", "-n", nutOnlyNS)).To(Succeed())
	Expect(string(driverConfig.Data["ups.conf"])).NotTo(ContainSubstring("authconf"))
	Expect(string(driverConfig.Data["ups.conf"])).To(ContainSubstring("mode = repeater"))

	// A closed loopback port is deterministic and never reaches an external machine.
	late := nutOnlyDevice("mod4-late")
	late.Spec.Driver = ""
	late.Spec.UpstreamNUT = &power.UPSUpstreamNUTSpec{Host: "127.0.0.1", Port: ptr.To(int32(3494)), UPSName: "absent", StrictStart: ptr.To(true)}
	lateServer := nutOnlyServer("mod4-late", late.Name)
	Expect(applyFixtureManifest(logicalFlowJSONList(late, lateServer))).To(Succeed())
	var latePod corev1.Pod
	Eventually(func(g Gomega) {
		var pods corev1.PodList
		g.Expect(logicalFlowGet(&pods, "pods", "-n", nutOnlyNS, "-l", "power.zalud.io/nutserver=mod4-late")).To(Succeed())
		g.Expect(pods.Items).To(HaveLen(1))
		latePod = pods.Items[0]
		out, err := utils.Run(exec.Command("kubectl", "logs", "-n", nutOnlyNS, latePod.Name, "-c", "driver-supervisor"))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(ContainSubstring("driver termination"))
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
	late.Spec.UpstreamNUT.StrictStart = ptr.To(false)
	Expect(applyFixtureManifest(logicalFlowJSONList(late))).To(Succeed())
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "exec", "-n", nutOnlyNS, latePod.Name, "-c", "driver-supervisor", "--", "sh", "-c", recoveryDriverSampleScript))
		g.Expect(err).NotTo(HaveOccurred())
		_, err = parseRecoveryDriver(out)
		g.Expect(err).NotTo(HaveOccurred())
	}, 3*time.Minute, 2*time.Second).Should(Succeed())
	var status power.NUTServer
	Expect(logicalFlowGet(&status, "nutserver", lateServer.Name)).To(Succeed())
	Expect(status.Status.Phase).NotTo(Equal(power.NUTServerPhaseReady))
	// upsd can briefly serve its WAIT placeholder while connecting to the new
	// driver, before processing DATASTALE. Permit only that placeholder during
	// convergence; an actual UPS status must fail immediately, never be retried.
	checkRead := func(g Gomega, allowWaiting bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", nutOnlyNS, latePod.Name,
			"-c", "upsd", "--", "upsc", "mod4-late@127.0.0.1", "ups.status")
		// Keep stdout separate from TLS initialization messages on stderr.
		out, err := cmd.Output()
		if err == nil && allowWaiting {
			Expect(strings.TrimSpace(string(out))).To(Equal("WAIT"), "unavailable relay returned telemetry")
		}
		g.Expect(err).To(HaveOccurred(), "relay read succeeded: %q", out)
		var exitErr *exec.ExitError
		g.Expect(errors.As(err, &exitErr)).To(BeTrue())
		g.Expect(strings.ToLower(string(exitErr.Stderr))).To(
			Or(ContainSubstring("data stale"), ContainSubstring("driver not connected")))
	}
	Eventually(func(g Gomega) { checkRead(g, true) }, 30*time.Second, time.Second).Should(Succeed())
	Consistently(func(g Gomega) { checkRead(g, false) }, 10*time.Second, 2*time.Second).Should(Succeed())
}
