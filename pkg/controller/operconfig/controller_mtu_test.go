package operconfig_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/utils/ptr"
)

func testMTUProbe() {
	t := newTestDriver()

	var watcher *watch.FakeWatcher

	JustBeforeEach(func() {
		watcher = t.fakeCache.AwaitWatcher(&operv1.Network{})
	})

	When("the MTU isn't configured in the operator config", func() {
		BeforeEach(func() {
			t.operConfig.Spec.DefaultNetwork.OVNKubernetesConfig.MTU = nil
			t.mtuConfigMap = nil
		})

		It("should deploy the MTU probe Job", func(ctx context.Context) {
			watcher.Add(t.operConfig)

			var job *uns.Unstructured

			// Wait for the Job to be created.
			Eventually(func(g Gomega) {
				var err error
				job, err = t.fakeClient.Default().Dynamic().Resource(batchv1.SchemeGroupVersion.WithResource("jobs")).
					Namespace(names.APPLIED_NAMESPACE).Get(ctx, "mtu-prober", metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred(), "mtu-prober Job not found")
			}).Within(5 * time.Second).Should(Succeed())

			// Kludgy but the controller creates the Job in the dynamic client but deletes from the CR client so mirror it there.
			Expect(t.fakeClient.Default().CRClient().Create(ctx, job)).To(Succeed())

			// The controller waits for the Job to create MTU ConfigMap so simulate that here.
			Expect(t.fakeClient.Default().CRClient().Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      util.MTU_CM_NAME,
					Namespace: util.MTU_CM_NAMESPACE,
				},
				Data: map[string]string{"mtu": "5000"},
			})).To(Succeed())

			// The controller should delete the Job.
			Eventually(func(g Gomega) {
				err := t.fakeClient.Default().CRClient().Get(ctx, types.NamespacedName{
					Name: job.GetName(), Namespace: job.GetNamespace()}, job)
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "mtu-prober Job not deleted")
			}).Within(5 * time.Second).Should(Succeed())

			t.awaitAnyClusterOperatorConditions(ctx)

			// The controller should update the MTU in the operator config.
			Expect(ptr.Deref(t.getOperConfig(ctx).Spec.DefaultNetwork.OVNKubernetesConfig.MTU, 0)).NotTo(BeZero())
		})

		Context("and deploying the Job fails", func() {
			JustBeforeEach(func() {
				t.setupDynamicClientFailOnAction("patch", "jobs")
				watcher.Add(t.operConfig)
			})

			t.testDegradedStatusCondition()
		})
	})

	When("the MTU is already configured in the operator config", func() {
		It("should not deploy the MTU probe Job", func(ctx context.Context) {
			watcher.Add(t.operConfig)

			Consistently(func(g Gomega) {
				_, err := t.fakeClient.Default().Dynamic().Resource(batchv1.SchemeGroupVersion.WithResource("jobs")).
					Namespace(names.APPLIED_NAMESPACE).Get(ctx, "mtu-prober", metav1.GetOptions{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			}).Within(500 * time.Millisecond).Should(Succeed())
		})
	})
}
