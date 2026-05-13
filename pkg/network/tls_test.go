package network

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
)

var _ = Describe("addTLSInfoToRenderData", func() {
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
				addTLSInfoToRenderData(data, bootstrapResult, respectAdherence)

				Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))
				Expect(data).To(HaveKeyWithValue(TLSMinVersionKey, configv1.VersionTLS12))
				Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384"))
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
					addTLSInfoToRenderData(data, bootstrapResult, true)

					Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, false))
					Expect(data).NotTo(HaveKey(TLSMinVersionKey))
					Expect(data).NotTo(HaveKey(TLSCipherSuitesKey))
				})
			})

			Context("and not respecting adherence", func() {
				It("should use the TLS profile info", func() {
					addTLSInfoToRenderData(data, bootstrapResult, false)

					Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))
					Expect(data).To(HaveKeyWithValue(TLSMinVersionKey, configv1.VersionTLS13))
					Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256"))
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

			addTLSInfoToRenderData(data, bootstrapResult, false)

			Expect(data[UseTLSProfileKey]).To(BeTrue())
			Expect(data[TLSMinVersionKey]).To(Equal(configv1.VersionTLS12))
			Expect(data[TLSCipherSuitesKey]).To(BeEmpty())
		})
	})
})
