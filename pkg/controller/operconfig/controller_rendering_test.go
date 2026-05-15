package operconfig_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"
	cohelpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

func testRendering() {
	t := newTestDriver()

	var watcher *watch.FakeWatcher

	JustBeforeEach(func() {
		watcher = t.fakeCache.AwaitWatcher(&operv1.Network{})
	})

	Specify("reconciliation should render and apply component resources", func(ctx context.Context) {
		watcher.Add(t.operConfig)

		t.awaitClusterOperatorConditions(ctx, func(g Gomega, conditions []configv1.ClusterOperatorStatusCondition) {
			g.Expect(cohelpers.IsStatusConditionTrue(conditions, configv1.OperatorAvailable)).To(BeTrue())
			g.Expect(cohelpers.IsStatusConditionFalse(conditions, configv1.OperatorProgressing)).To(BeTrue())
			g.Expect(cohelpers.IsStatusConditionFalse(conditions, configv1.OperatorDegraded)).To(BeTrue())
		})

		// There's a lot of resources created, just verify some of them.
		for _, r := range []struct {
			gvr       schema.GroupVersionResource
			name      string
			namespace string
			verify    func(*uns.Unstructured)
		}{
			{
				gvr:  corev1.SchemeGroupVersion.WithResource("namespaces"),
				name: "openshift-multus",
			},
			{
				gvr:       appsv1.SchemeGroupVersion.WithResource("daemonsets"),
				name:      "ovnkube-node",
				namespace: util.OVN_NAMESPACE,
				verify: func(obj *uns.Unstructured) {
					Expect(obj.GetLabels()).To(HaveKeyWithValue(names.GenerateStatusLabel, names.StandAloneClusterName))
					Expect(obj.GetOwnerReferences()).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
						"APIVersion": Equal(operv1.GroupVersion.String()),
						"Kind":       Equal("Network"),
						"Name":       Equal(t.operConfig.Name),
					})))
				},
			},
			{
				gvr:  configv1.GroupVersion.WithResource("networks"),
				name: names.CLUSTER_CONFIG,
				verify: func(obj *uns.Unstructured) {
					c := configv1.Network{}
					Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), &c)).To(Succeed())
					Expect(c.Status.NetworkType).To(Equal(string(t.operConfig.Spec.DefaultNetwork.Type)))
					Expect(c.Status.ServiceNetwork).To(Equal(t.clusterConfig.Spec.ServiceNetwork))
				},
			},
		} {
			obj, err := t.fakeClient.Default().Dynamic().Resource(r.gvr).Namespace(r.namespace).Get(ctx, r.name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred(), "failed to get %s %s", r.gvr.Resource, r.name)

			if r.verify != nil {
				r.verify(obj)
			}
		}
	})

	When("the operator config ManagementState is Unmanaged", func() {
		BeforeEach(func() {
			t.operConfig.Spec.ManagementState = operv1.Unmanaged
		})

		Specify("reconciliation should skip rendering", func(ctx context.Context) {
			watcher.Add(t.operConfig)
			t.ensureNoClusterOperatorConditions(ctx)
		})
	})

	When("OVNK IPsec mode is enabled", func() {
		BeforeEach(func() {
			t.operConfig.Spec.DefaultNetwork.OVNKubernetesConfig.IPsecConfig = &operv1.IPsecConfig{
				Mode: operv1.IPsecModeFull,
			}
		})

		JustBeforeEach(func(ctx context.Context) {
			// The ClusterOperator being available enables MachineConfigs to be created.
			mcoClusterOp := &configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "machine-config"},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
					},
					Versions: []configv1.OperandVersion{
						{
							Name:    "operator",
							Version: "4.22.0",
						},
					},
				},
			}
			Expect(t.fakeClient.Default().CRClient().Create(ctx, mcoClusterOp)).To(Succeed())

			watcher.Add(t.operConfig)
		})

		Specify("reconciliation should apply MachineConfigs", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				_, err := t.fakeClient.Default().Dynamic().Resource(mcfgv1.GroupVersion.WithResource("machineconfigs")).
					Get(ctx, "80-ipsec-worker-extensions", metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred(), "Machine Config was not created")
			}).Within(5 * time.Second).Should(Succeed())
		})
	})
}
