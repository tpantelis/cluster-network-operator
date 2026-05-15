package operconfig_test

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func testAppliedConfig() {
	t := newTestDriver()

	var prevSpec *operv1.NetworkSpec

	BeforeEach(func() {
		prevSpec = t.operConfig.Spec.DeepCopy()
		prevSpec.ClusterNetwork = []operv1.ClusterNetworkEntry{
			{
				CIDR:       t.clusterConfig.Spec.ClusterNetwork[0].CIDR,
				HostPrefix: t.clusterConfig.Spec.ClusterNetwork[0].HostPrefix,
			},
		}
		prevSpec.ServiceNetwork = t.clusterConfig.Spec.ServiceNetwork
	})

	JustBeforeEach(func(ctx context.Context) {
		if prevSpec != nil {
			appliedJSON, err := json.Marshal(prevSpec)
			Expect(err).NotTo(HaveOccurred())

			appliedConfigCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      names.APPLIED_PREFIX + names.OPERATOR_CONFIG,
					Namespace: names.APPLIED_NAMESPACE,
				},
				Data: map[string]string{
					"applied": string(appliedJSON),
				},
			}

			Expect(t.fakeClient.Default().CRClient().Create(ctx, appliedConfigCM)).To(Succeed())
		}

		t.fakeCache.AwaitWatcher(&operv1.Network{}).Add(t.operConfig)
	})

	awaitAppliedSpec := func(ctx context.Context) *operv1.NetworkSpec {
		var obj *uns.Unstructured

		Eventually(func(g Gomega) {
			var err error

			obj, err = t.fakeClient.Default().Dynamic().Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
				Namespace(names.APPLIED_NAMESPACE).Get(ctx, names.APPLIED_PREFIX+names.OPERATOR_CONFIG, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
		}).Within(5 * time.Second).Should(Succeed())

		cm := corev1.ConfigMap{}
		Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), &cm)).To(Succeed())
		Expect(cm.Data).To(HaveKey("applied"))

		var appliedSpec operv1.NetworkSpec
		Expect(json.Unmarshal([]byte(cm.Data["applied"]), &appliedSpec)).To(Succeed())

		return &appliedSpec
	}

	When("a previous applied config exists", func() {
		BeforeEach(func() {
			t.operConfig.Spec.DefaultNetwork.OVNKubernetesConfig.MTU = nil
			prevSpec.DefaultNetwork.OVNKubernetesConfig.MTU = ptr.To[uint32](1401)
		})

		It("should detect and apply changes", func(ctx context.Context) {
			appliedSpec := awaitAppliedSpec(ctx)
			Expect(ptr.Deref(appliedSpec.DefaultNetwork.OVNKubernetesConfig.MTU, 0)).To(Equal(uint32(1401)))

			Eventually(func(g Gomega) {
				g.Expect(ptr.Deref(t.getOperConfig(ctx).Spec.DefaultNetwork.OVNKubernetesConfig.MTU, 0)).NotTo(BeZero())
			}).Within(5 * time.Second).Should(Succeed())
		})

		Context("but the network change isn't safe", func() {
			BeforeEach(func() {
				// Changing from IPv4 -> IPv6 isn't allowed.
				prevSpec.ServiceNetwork = []string{"fd02::/112"}
			})

			t.testDegradedStatusCondition()
		})
	})

	When("no previous applied config exists", func() {
		BeforeEach(func() {
			prevSpec = nil
		})

		It("should create a new applied config", func(ctx context.Context) {
			appliedSpec := awaitAppliedSpec(ctx)
			Expect(appliedSpec.ManagementState).To(Equal(operv1.Managed))
			Expect(appliedSpec.ServiceNetwork).To(Equal(t.clusterConfig.Spec.ServiceNetwork))
			Expect(appliedSpec.DisableMultiNetwork).NotTo(BeNil())
			Expect(*appliedSpec.DisableMultiNetwork).To(BeFalse())
		})
	})
}
