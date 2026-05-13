package network_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/network"
)

var _ = Describe("AddTLSInfoToRenderData", func() {
	var (
		data            map[string]interface{}
		bootstrapResult *bootstrap.BootstrapResult
	)

	BeforeEach(func() {
		data = make(map[string]interface{})
		bootstrapResult = &bootstrap.BootstrapResult{}
	})

	DescribeTableSubtree("when adherence policy is StrictAllComponents",
		func(respectAdherence bool) {
			BeforeEach(func() {
				bootstrapResult.TLSProfile = bootstrap.TLSProfile{
					Spec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
					},
					Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
				}
			})

			It("should use the TLS profile info", func() {
				network.AddTLSInfoToRenderData(data, bootstrapResult, respectAdherence)

				Expect(data).To(HaveKeyWithValue(network.UseTLSProfileKey, true))
				Expect(data).To(HaveKeyWithValue(network.TLSMinVersionKey, configv1.VersionTLS12))
				Expect(data).To(HaveKeyWithValue(network.TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384"))
			})
		},
		Entry("and respecting adherence", true),
		Entry("and not respecting adherence", false),
	)

	DescribeTableSubtree("adherence policy is",
		func(adherence configv1.TLSAdherencePolicy) {
			BeforeEach(func() {
				bootstrapResult.TLSProfile = bootstrap.TLSProfile{
					Spec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS13,
						Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
					},
					Adherence: adherence,
				}
			})

			Context("and respecting adherence", func() {
				It("should not use the TLS profile info", func() {
					network.AddTLSInfoToRenderData(data, bootstrapResult, true)

					Expect(data).To(HaveKeyWithValue(network.UseTLSProfileKey, false))
					Expect(data).NotTo(HaveKey(network.TLSMinVersionKey))
					Expect(data).NotTo(HaveKey(network.TLSCipherSuitesKey))
				})
			})

			Context("and not respecting adherence", func() {
				It("should use the TLS profile info", func() {
					network.AddTLSInfoToRenderData(data, bootstrapResult, false)

					Expect(data).To(HaveKeyWithValue(network.UseTLSProfileKey, true))
					Expect(data).To(HaveKeyWithValue(network.TLSMinVersionKey, configv1.VersionTLS13))
					Expect(data).To(HaveKeyWithValue(network.TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256"))
				})
			})
		},
		Entry("LegacyAdheringComponentsOnly", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly),
		Entry("NoOpinion (empty)", configv1.TLSAdherencePolicyNoOpinion),
	)

	Context("with nil cipher list", func() {
		It("should set empty cipher suites string", func() {
			bootstrapResult.TLSProfile = bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
				},
			}

			network.AddTLSInfoToRenderData(data, bootstrapResult, false)

			Expect(data[network.UseTLSProfileKey]).To(BeTrue())
			Expect(data[network.TLSMinVersionKey]).To(Equal(configv1.VersionTLS12))
			Expect(data[network.TLSCipherSuitesKey]).To(BeEmpty())
		})
	})
})

func TestTLS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Network Suite")
}
