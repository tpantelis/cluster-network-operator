package operconfig_test

import (
	"context"
	"fmt"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testOperatorConfigWatch() {
	t := newTestDriver()

	var watcher *watch.FakeWatcher

	JustBeforeEach(func(ctx context.Context) {
		watcher = t.fakeCache.AwaitWatcher(&operv1.Network{})
	})

	When("the resource is added", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			watcher.Add(t.operConfig)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("the network spec is changed", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(t.operConfig.DeepCopy())
			t.operConfig.Spec.ServiceNetwork = append(t.operConfig.Spec.ServiceNetwork, "173.30.0.0/16")
			watcher.Modify(t.operConfig)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("the resource is updated but the network spec is unchanged", func() {
		It("should not trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(t.operConfig.DeepCopy())
			t.operConfig.Status.ReadyReplicas = 2
			watcher.Modify(t.operConfig)
			t.ensureNoClusterOperatorConditions(ctx)
		})
	})
}

func testClusterConfigWatch() {
	t := newTestDriver()

	var watcher *watch.FakeWatcher

	JustBeforeEach(func(ctx context.Context) {
		watcher = t.fakeCache.AwaitWatcher(&configv1.Network{})
	})

	When("the resource is added", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			watcher.Add(t.clusterConfig)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("the NetworkDiagnostics is changed", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(t.clusterConfig.DeepCopy())
			t.clusterConfig.Spec.NetworkDiagnostics.Mode = configv1.NetworkDiagnosticsAll
			watcher.Modify(t.clusterConfig)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("the resource is updated but the NetworkDiagnostics is unchanged", func() {
		It("should not trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(t.clusterConfig.DeepCopy())
			t.clusterConfig.Status.ClusterNetworkMTU = 100
			watcher.Modify(t.clusterConfig)
			t.ensureNoClusterOperatorConditions(ctx)
		})
	})
}

func testNodeWatch() {
	t := newTestDriver()

	var (
		watcher *watch.FakeWatcher
		node    *corev1.Node
	)

	BeforeEach(func() {
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node"}}
	})

	JustBeforeEach(func(ctx context.Context) {
		watcher = t.fakeCache.AwaitWatcher(&corev1.Node{})
	})

	When("a Node is added", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			watcher.Add(node)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("a Node's labels are changed", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(node.DeepCopy())
			node.Labels = map[string]string{"label": "value"}
			watcher.Modify(node)
			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	When("a Node is updated but the labels are unchanged", func() {
		It("should not trigger reconciliation", func(ctx context.Context) {
			t.fakeCache.SeedInformerStore(node.DeepCopy())
			node.Status.Phase = corev1.NodeRunning
			watcher.Modify(node)
			t.ensureNoClusterOperatorConditions(ctx)
		})
	})
}

func testConfigMapWatch() {
	t := newTestDriver()

	var configMap *corev1.ConfigMap

	BeforeEach(func() {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: names.APPLIED_NAMESPACE,
			},
			Data: map[string]string{"key": "value"},
		}
	})

	JustBeforeEach(func() {
		// There's a race condition in fake clients where add events may be missed if objects are created concurrently
		// with informer startup so ensure the watch has been registered first.
		Eventually(func(g Gomega) {
			g.Expect(slices.IndexFunc(t.fakeClient.Default().Kubernetes().(*fakek8s.Clientset).Actions(), func(a k8stesting.Action) bool {
				return a.GetVerb() == "watch" && a.GetResource().Resource == "configmaps"
			})).To(BeNumerically(">=", 0))
		}).Should(Succeed(), "Watch action not received")
	})

	When("a ConfigMap is added/modified", func() {
		It("should trigger reconciliation", func(ctx context.Context) {
			created, err := t.fakeClient.Default().Kubernetes().CoreV1().ConfigMaps(names.APPLIED_NAMESPACE).Create(ctx, configMap,
				metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			t.awaitAnyClusterOperatorConditions(ctx)

			co := t.getClusterOperator(ctx)
			co.Status.Conditions = nil
			Expect(t.fakeClient.Default().CRClient().Update(ctx, co)).To(Succeed())

			created.Data["key"] = "newvalue"
			_, err = t.fakeClient.Default().Kubernetes().CoreV1().ConfigMaps(names.APPLIED_NAMESPACE).Update(ctx, created,
				metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())

			t.awaitAnyClusterOperatorConditions(ctx)
		})
	})

	DescribeTableSubtree("",
		func(name string) {
			BeforeEach(func() {
				configMap.Name = name
			})

			When(fmt.Sprintf("the %s ConfigMap is modified", name), func() {
				It("should not trigger reconciliation", func(ctx context.Context) {
					created, err := t.fakeClient.Default().Kubernetes().CoreV1().ConfigMaps(names.APPLIED_NAMESPACE).Create(ctx, configMap,
						metav1.CreateOptions{})
					Expect(err).NotTo(HaveOccurred())

					created.Data["key"] = "newvalue"
					_, err = t.fakeClient.Default().Kubernetes().CoreV1().ConfigMaps(names.APPLIED_NAMESPACE).Update(ctx, created,
						metav1.UpdateOptions{})
					Expect(err).NotTo(HaveOccurred())
					t.ensureNoClusterOperatorConditions(ctx)
				})
			})
		},
		Entry("", "network-operator-lock"),
		Entry("", "applied-cluster"),
	)
}
