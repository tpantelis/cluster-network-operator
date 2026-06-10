package network

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/crypto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// UseTLSProfileKey is the template data key indicating whether to use the cluster TLS profile
	UseTLSProfileKey = "UseTLSProfile"
	// TLSMinVersionKey is the template data key for the minimum TLS version
	TLSMinVersionKey = "TLSMinVersion"
	// TLSCipherSuitesKey is the template data key for the comma-separated cipher suites
	TLSCipherSuitesKey = "TLSCipherSuites"
)

// addTLSInfoToRenderData adds TLS-related template data to the render data.
func addTLSInfoToRenderData(data map[string]interface{}, bootstrapResult *bootstrap.BootstrapResult, respectAdherence bool) {
	if respectAdherence && !crypto.ShouldHonorClusterTLSProfile(bootstrapResult.TLSProfile.Adherence) {
		data[UseTLSProfileKey] = false
		return
	}

	data[TLSMinVersionKey] = bootstrapResult.TLSProfile.Spec.MinTLSVersion
	data[TLSCipherSuitesKey] = strings.Join(bootstrapResult.TLSProfile.Spec.Ciphers, ",")
	data[UseTLSProfileKey] = true
}

func getTLSProfile(client cnoclient.Client) (bootstrap.TLSProfile, error) {
	hc := hypershift.NewHyperShiftConfig()
	if hc.Enabled {
		// For HyperShift, read TLS profile from HostedCluster CR in the management cluster
		return getTLSProfileFromHostedCluster(client, hc)
	}

	// For non-HyperShift, read from APIServer CR in the default cluster
	apiServer := &configv1.APIServer{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: openshifttls.APIServerName}, apiServer); err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to fetch apiserver.config.openshift.io/%s: %w", openshifttls.APIServerName, err)
	}

	return toTLSProfile(&apiServer.Spec)
}

func getTLSProfileFromHostedCluster(client cnoclient.Client, hc *hypershift.HyperShiftConfig) (bootstrap.TLSProfile, error) {
	// Fetch HostedCluster CR from management cluster
	hostedCluster, err := client.ClientFor(names.ManagementClusterName).Dynamic().
		Resource(hypershift.HostedClusterGVR).Namespace(hc.Namespace).Get(context.TODO(), hc.Name, metav1.GetOptions{})
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to fetch HostedCluster %s/%s: %w", hc.Namespace, hc.Name, err)
	}

	apiServerConfig, found, err := unstructured.NestedFieldCopy(hostedCluster.UnstructuredContent(), "spec", "configuration", "apiServer")
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to extract apiServer from HostedCluster: %w", err)
	}

	var apiServerSpec configv1.APIServerSpec
	if found && apiServerConfig != nil {
		apiServerMap, ok := apiServerConfig.(map[string]interface{})
		if !ok {
			return bootstrap.TLSProfile{}, fmt.Errorf("invalid HostedCluster apiServer type: %T", apiServerConfig)
		}

		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(apiServerMap, &apiServerSpec); err != nil {
			return bootstrap.TLSProfile{}, fmt.Errorf("failed to convert apiServer spec: %w", err)
		}
	}

	return toTLSProfile(&apiServerSpec)
}

func toTLSProfile(apiServerSpec *configv1.APIServerSpec) (bootstrap.TLSProfile, error) {
	profileSpec, err := openshifttls.GetTLSProfileSpec(apiServerSpec.TLSSecurityProfile)
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to get TLS profile spec: %w", err)
	}

	return bootstrap.TLSProfile{
		Spec:      profileSpec,
		Adherence: apiServerSpec.TLSAdherence,
	}, nil
}
