package network_test

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	fakeclient "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Bootstrap", func() {
	var (
		operConfig *operv1.Network
		clientObjs []crclient.Object
		client     cnoclient.Client
	)

	BeforeEach(func() {
		operConfig = &operv1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
			Spec: operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						MTU: nil,
					},
				},
			},
		}

		clientObjs = []crclient.Object{
			&configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.NonePlatformType,
					},
				},
			},
			&configv1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: openshifttls.APIServerName},
				Spec: configv1.APIServerSpec{
					TLSSecurityProfile: &configv1.TLSSecurityProfile{
						Type: configv1.TLSProfileCustomType,
						Custom: &configv1.CustomTLSProfile{
							TLSProfileSpec: configv1.TLSProfileSpec{
								MinTLSVersion: configv1.VersionTLS13,
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
							},
						},
					},
					TLSAdherence: configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
				},
			},
			&configv1.Proxy{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			},
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      network.CLUSTER_CONFIG_NAME,
					Namespace: network.CLUSTER_CONFIG_NAMESPACE,
				},
				Data: map[string]string{
					"install-config": "controlPlane:\n  replicas: 3\n",
				},
			}}
	})

	JustBeforeEach(func() {
		client = fakeclient.NewFakeClient(clientObjs...)
	})

	assertBootstrapSuccess := func() *bootstrap.BootstrapResult {
		result, err := network.Bootstrap(operConfig, client)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).NotTo(BeNil())

		return result
	}

	It("should set the InfraStatus", func(ctx context.Context) {
		result := assertBootstrapSuccess()
		Expect(result.Infra.PlatformType).To(Equal(configv1.NonePlatformType))
	})

	It("should bootstrap OVN when network type is OVNKubernetes", func(ctx context.Context) {
		result := assertBootstrapSuccess()
		Expect(result.OVN).NotTo(BeNil())
	})

	Context("in standalone (non-HyperShift) mode", func() {
		It("should set the TLS profile info from the APIServer CR", func(ctx context.Context) {
			result := assertBootstrapSuccess()
			Expect(result.TLSProfile.Spec.MinTLSVersion).To(Equal(configv1.VersionTLS13))
			Expect(result.TLSProfile.Spec.Ciphers).To(ConsistOf("TLS_AES_128_GCM_SHA256"))
			Expect(result.TLSProfile.Adherence).To(Equal(configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly))
		})
	})

	Context("in HyperShift mode", func() {
		const (
			hostedClusterName      = "test-hosted-cluster"
			hostedClusterNamespace = "test-namespace"
		)

		BeforeEach(func() {
			// Set HyperShift environment variables
			Expect(os.Setenv("HYPERSHIFT", "true")).To(Succeed())
			Expect(os.Setenv("HOSTED_CLUSTER_NAME", hostedClusterName)).To(Succeed())
			Expect(os.Setenv("HOSTED_CLUSTER_NAMESPACE", hostedClusterNamespace)).To(Succeed())

			DeferCleanup(func() {
				os.Unsetenv("HYPERSHIFT")
				os.Unsetenv("HOSTED_CLUSTER_NAME")
				os.Unsetenv("HOSTED_CLUSTER_NAMESPACE")
			})
		})

		JustBeforeEach(func(ctx context.Context) {
			hcp := &uns.Unstructured{}
			hcp.SetGroupVersionKind(hypershift.HostedControlPlaneGVK)
			hcp.SetName(hostedClusterName)
			hcp.SetNamespace(hostedClusterNamespace)

			Expect(client.ClientFor(names.ManagementClusterName).CRClient().Create(ctx, hcp)).To(Succeed())
		})

		When("the HostedCluster CR exists", func() {
			var hostedCluster *uns.Unstructured

			BeforeEach(func() {
				hostedCluster = &uns.Unstructured{}
				hostedCluster.SetGroupVersionKind(hypershift.HostedClusterGVK)
				hostedCluster.SetName(hostedClusterName)
				hostedCluster.SetNamespace(hostedClusterNamespace)
				hostedCluster.Object["spec"] = map[string]interface{}{
					"configuration": map[string]interface{}{
						"apiServer": map[string]interface{}{
							"tlsSecurityProfile": map[string]interface{}{
								"type": string(configv1.TLSProfileModernType),
							},
							"tlsAdherence": string(configv1.TLSAdherencePolicyStrictAllComponents),
						},
					},
				}
			})

			JustBeforeEach(func(ctx context.Context) {
				_, err := client.ClientFor(names.ManagementClusterName).Dynamic().Resource(hypershift.HostedClusterGVR).
					Namespace(hostedCluster.GetNamespace()).Create(ctx, hostedCluster, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred())
			})

			It("should set the TLS profile info from the APIServer spec", func(ctx context.Context) {
				result := assertBootstrapSuccess()
				Expect(result.TLSProfile.Spec.MinTLSVersion).To(Equal(configv1.VersionTLS13))
				Expect(result.TLSProfile.Spec.Ciphers).NotTo(BeEmpty())
				Expect(result.TLSProfile.Adherence).To(Equal(configv1.TLSAdherencePolicyStrictAllComponents))
			})

			Context("and the APIServer spec doesn't exist", func() {
				BeforeEach(func() {
					hostedCluster.Object["spec"] = map[string]interface{}{}
				})

				It("should set the TLS profile info from the defaults", func(ctx context.Context) {
					result := assertBootstrapSuccess()
					Expect(result.TLSProfile.Spec.MinTLSVersion).To(Equal(configv1.VersionTLS12))
					Expect(result.TLSProfile.Spec.Ciphers).NotTo(BeEmpty())
					Expect(result.TLSProfile.Adherence).To(BeEmpty())
				})
			})
		})

		It("should return an error when the HostedCluster CR is not found", func(ctx context.Context) {
			_, err := network.Bootstrap(operConfig, client)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("IPTables alerter", func() {
		It("should be enabled in the BootstrapResult by default", func(ctx context.Context) {
			result, err := network.Bootstrap(operConfig, client)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())

			Expect(result.IPTablesAlerter.Enabled).To(BeTrue())
		})

		When("the ConfigMap exists and specifies disabled", func() {
			BeforeEach(func() {
				clientObjs = append(clientObjs, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "iptables-alerter-config",
						Namespace: "openshift-network-operator",
					},
					Data: map[string]string{
						"enabled": "false",
					},
				})
			})

			It("should be disabled in the BootstrapResult", func(ctx context.Context) {
				result, err := network.Bootstrap(operConfig, client)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).NotTo(BeNil())

				Expect(result.IPTablesAlerter.Enabled).To(BeFalse())
			})
		})
	})
})
