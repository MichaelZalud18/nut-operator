package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Exercise the production mapper/predicate with an actual Secret informer. The probe reconciler
// validates material and computes the production digest; no operand Deployment or NUT process runs.
func TestNUTServerTLSSecretInformer(t *testing.T) {
	environment := &envtest.Environment{BinaryAssetsDirectory: getFirstFoundEnvTestBinaryDir()}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := nutServerWatchScheme(t)
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: server.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	nutServer := tlsEnabledNUTServer()
	mapper := &NUTServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(nutServer).Build()}
	material := &NUTServerReconciler{Client: api}
	type observation struct {
		digest string
		err    error
	}
	observed := make(chan observation, 32)
	err = ctrl.NewControllerManagedBy(mgr).Named("tls-secret-watch-test").
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(mapper.nutServerRequestsForSecret), builder.WithPredicates(secretDataChangedPredicate())).
		Complete(reconcile.Func(func(ctx context.Context, _ reconcile.Request) (ctrl.Result, error) {
			err := material.validateNUTServerTLSSecrets(ctx, nutServer, "power-system")
			var digest string
			if err == nil {
				digest, err = material.nutServerTLSMaterialDigest(ctx, nutServer, "power-system")
			}
			select {
			case observed <- observation{digest, err}:
			case <-ctx.Done():
			}
			return ctrl.Result{}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-stopped:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager did not stop")
		}
	})
	ready, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	if !mgr.GetCache().WaitForCacheSync(ready) {
		t.Fatal("cache did not start")
	}
	if err := api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "power-system"}}); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nutServer.Spec.TLS.ServerCertificateRef.Name, Namespace: "power-system"}, Data: map[string][]byte{"tls.crt": []byte("fixture-cert-one"), "tls.key": []byte("fixture-key")}}
	await := func(accept func(observation) bool) observation {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-observed:
				if accept(got) {
					return got
				}
			case <-timer.C:
				t.Fatal("Secret change did not reach material reconciliation")
				return observation{}
			}
		}
	}
	if err := api.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	initial := await(func(o observation) bool { return o.err == nil && o.digest != "" })
	secret.Data["tls.crt"] = []byte("fixture-cert-two")
	if err := api.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	rotated := await(func(o observation) bool { return o.err == nil && o.digest != "" && o.digest != initial.digest })
	if err := api.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	await(func(o observation) bool { return o.err != nil })
	secret.ResourceVersion = ""
	secret.UID = ""
	if err := api.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	await(func(o observation) bool { return o.err == nil && o.digest == rotated.digest })
}
