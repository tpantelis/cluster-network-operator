package operconfig_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	fakeclient "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/operconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	cohelpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const clusterOperatorName = "cluster-network-operator"

func TestOperConfigController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OperConfig Controller Suite")
}

var _ = BeforeSuite(func() {
	for _, kind := range []string{"ServiceMonitor", "PrometheusRule"} {
		scheme.Scheme.AddKnownTypeWithName(
			schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: kind},
			&uns.Unstructured{},
		)
		scheme.Scheme.AddKnownTypeWithName(
			schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: kind + "List"},
			&uns.UnstructuredList{},
		)
	}
})

var _ = Describe("ReconcileOperConfig", func() {
	Context("Operator Config Watch", testOperatorConfigWatch)
	Context("Cluster Config Watch", testClusterConfigWatch)
	Context("Node Watch", testNodeWatch)
	Context("ConfigMap Watch", testConfigMapWatch)
	Context("Rendering", testRendering)
	Context("MTU probe", testMTUProbe)
	Context("Applied Config", testAppliedConfig)
	Context("Reconciliation failures", testFailures)
	Context("Hypershift Mode", testHypershiftMode)
})

type testDriver struct {
	clusterConfig  *configv1.Network
	operConfig     *operv1.Network
	infrastructure *configv1.Infrastructure
	fakeCache      *fake.Cache
	statusManager  *statusmanager.StatusManager
	fakeClient     cnoclient.Client
	mtuConfigMap   *corev1.ConfigMap
}

func newTestDriver() *testDriver {
	t := &testDriver{}

	BeforeEach(func() {
		t.clusterConfig = &configv1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
			Spec: configv1.NetworkSpec{
				NetworkType:    string(operv1.NetworkTypeOVNKubernetes),
				ServiceNetwork: []string{"172.30.0.0/16"},
				ClusterNetwork: []configv1.ClusterNetworkEntry{
					{
						CIDR:       "10.128.0.0/14",
						HostPrefix: 23,
					},
				},
			},
		}

		t.operConfig = &operv1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
			Spec: operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						MTU: ptr.To[uint32](1400),
					},
				},
				DeployKubeProxy: ptr.To(true),
			},
			Status: operv1.NetworkStatus{
				OperatorStatus: operv1.OperatorStatus{
					Conditions: []operv1.OperatorCondition{
						{
							Type:   operv1.OperatorStatusTypeAvailable,
							Status: operv1.ConditionTrue,
						},
					},
				},
			},
		}

		t.mtuConfigMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mtu",
				Namespace: "openshift-network-operator",
			},
		}

		t.infrastructure = &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.NonePlatformType,
				},
			},
		}
	})

	JustBeforeEach(func(ctx context.Context) {
		// Create cluster-config-v1 ConfigMap required by OVN bootstrap
		clusterConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      network.CLUSTER_CONFIG_NAME,
				Namespace: network.CLUSTER_CONFIG_NAMESPACE,
			},
			Data: map[string]string{
				"install-config": "controlPlane:\n  replicas: 3\n",
			},
		}

		t.fakeClient = fakeclient.NewFakeClient(t.infrastructure, clusterConfigMap,
			&configv1.Proxy{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}},
			&configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}})

		Expect(t.fakeClient.Default().CRClient().Create(ctx, t.clusterConfig)).To(Succeed())

		// Create in typed client for the statusManager
		_, err := t.fakeClient.Default().OpenshiftOperatorClient().OperatorV1().Networks().Create(ctx, t.operConfig, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		// Creating it in the CRClient for the controller
		Expect(t.fakeClient.Default().CRClient().Create(ctx, t.operConfig)).To(Succeed())

		if t.mtuConfigMap != nil {
			// Create MTU ConfigMap in CRClient to skip MTU probing
			Expect(t.fakeClient.Default().CRClient().Create(ctx, t.mtuConfigMap)).To(Succeed())
		}

		t.fakeCache = fake.NewCache(t.fakeClient.Default().CRClient())

		mgr, err := ctrl.NewManager(&rest.Config{}, manager.Options{
			NewCache: func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
				return t.fakeCache, nil
			},
			Controller: config.Controller{
				SkipNameValidation: ptr.To(true),
			},
			MapperProvider: func(_ *rest.Config, _ *http.Client) (meta.RESTMapper, error) {
				return t.fakeClient.Default().RESTMapper(), nil
			},
			Metrics: metricsserver.Options{
				BindAddress: "0", // Disable metrics server
			},
			HealthProbeBindAddress: "0", // Disable health probe server
			PprofBindAddress:       "0", // Disable pprof server
		})
		Expect(err).ToNot(HaveOccurred())

		t.statusManager = statusmanager.NewWithClock(t.fakeClient, clusterOperatorName, "", &FakeClock{})

		// Set the manifest path - try different paths depending on where the test is run from.
		manifestPath := operconfig.ManifestPath
		if _, err := os.Stat(manifestPath); err != nil {
			manifestPath = "../../../bindata"
		}

		// Register all feature gates that might be checked during reconciliation
		err = operconfig.AddWithManifestPath(mgr, t.statusManager, t.fakeClient,
			featuregates.NewFeatureGate(
				[]configv1.FeatureGateName{
					apifeatures.FeatureGateDNSNameResolver,
					apifeatures.FeatureGateNetworkConnect,
					apifeatures.FeatureGateOVNObservability,
					apifeatures.FeatureGateNoOverlayMode,
					apifeatures.FeatureGateEVPN,
					apifeatures.FeatureGateAWSDualStackInstall,
					apifeatures.FeatureGateAzureDualStackInstall,
					apifeatures.FeatureGateGCPDualStackInstall,
				},
				[]configv1.FeatureGateName{}), manifestPath)
		Expect(err).ToNot(HaveOccurred())

		ctx, cancel := context.WithCancel(context.Background())

		DeferCleanup(cancel)

		// Start custom informers (ie ConfigMap informer)
		Expect(t.fakeClient.Start(ctx)).To(Succeed())

		go func() {
			defer GinkgoRecover()

			err := mgr.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()
	})

	return t
}

func (t *testDriver) awaitClusterOperatorConditions(ctx context.Context, verify func(Gomega, []configv1.ClusterOperatorStatusCondition)) {
	Eventually(func(g Gomega) {
		co := t.getClusterOperator(ctx)
		g.Expect(co.Status.Conditions).ToNot(BeEmpty(), "status conditions should be set")

		if verify != nil {
			verify(g, co.Status.Conditions)
		}
	}).Within(5 * time.Second).ProbeEvery(50 * time.Millisecond).Should(Succeed())
}

func (t *testDriver) awaitAnyClusterOperatorConditions(ctx context.Context) {
	t.awaitClusterOperatorConditions(ctx, nil)
}

func (t *testDriver) ensureNoClusterOperatorConditions(ctx context.Context) {
	Consistently(func(g Gomega) {
		co := t.getClusterOperator(ctx)
		g.Expect(co.Status.Conditions).To(BeEmpty(), "status conditions should not be set")
	}).Within(500 * time.Millisecond).ProbeEvery(50 * time.Millisecond).Should(Succeed())
}

func (t *testDriver) getClusterOperator(ctx context.Context) *configv1.ClusterOperator {
	co := &configv1.ClusterOperator{}
	err := t.fakeClient.Default().CRClient().Get(ctx, types.NamespacedName{Name: clusterOperatorName}, co)

	Expect(err).NotTo(HaveOccurred())

	return co
}

func (t *testDriver) setupDynamicClientFailOnAction(verb, resource string) {
	t.fakeClient.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor(verb, resource,
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("mock error")
		})
}

func (t *testDriver) getOperConfig(ctx context.Context) *operv1.Network {
	obj, err := t.fakeClient.Default().Dynamic().Resource(operv1.GroupVersion.WithResource("networks")).
		Get(ctx, names.OPERATOR_CONFIG, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	operConfig := &operv1.Network{}
	Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), &operConfig)).To(Succeed())

	return operConfig
}

func (t *testDriver) testDegradedStatusCondition() {
	It("should set the Degraded status condition", func(ctx context.Context) {
		t.awaitClusterOperatorConditions(ctx, func(g Gomega, conditions []configv1.ClusterOperatorStatusCondition) {
			g.Expect(cohelpers.IsStatusConditionTrue(conditions, configv1.OperatorDegraded)).To(BeTrue())
		})
	})
}

type FakeClock struct {
}

func (f FakeClock) Now() time.Time {
	return time.Now()
}

// Since always returns 3 minutes to exceed the degradedFailureDurationThreshold (2 minutes)
// in StatusManager, allowing tests to immediately observe degraded conditions without waiting.
func (f FakeClock) Since(t time.Time) time.Duration {
	return time.Minute * 3
}
