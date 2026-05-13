package network

import (
	"strings"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/library-go/pkg/crypto"
)

const (
	// UseTLSProfileKey is the template data key indicating whether to use the cluster TLS profile
	UseTLSProfileKey = "UseTLSProfile"
	// TLSMinVersionKey is the template data key for the minimum TLS version
	TLSMinVersionKey = "TLSMinVersion"
	// TLSCipherSuitesKey is the template data key for the comma-separated cipher suites
	TLSCipherSuitesKey = "TLSCipherSuites"
)

// AddTLSInfoToRenderData adds TLS-related template data to the render data.
func AddTLSInfoToRenderData(data map[string]interface{}, bootstrapResult *bootstrap.BootstrapResult, respectAdherence bool) {
	if respectAdherence && !crypto.ShouldHonorClusterTLSProfile(bootstrapResult.TLSProfile.Adherence) {
		data[UseTLSProfileKey] = false
		return
	}

	data[TLSMinVersionKey] = bootstrapResult.TLSProfile.Spec.MinTLSVersion
	data[TLSCipherSuitesKey] = strings.Join(bootstrapResult.TLSProfile.Spec.Ciphers, ",")
	data[UseTLSProfileKey] = true
}
