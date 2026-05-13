package network

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Bootstrap creates resources required by the network plugin on the cloud.
func Bootstrap(conf *operv1.Network, client cnoclient.Client) (*bootstrap.BootstrapResult, error) {
	out := &bootstrap.BootstrapResult{}

	infraStatus, err := platform.InfraStatus(client)
	if err != nil {
		return nil, err
	}
	out.Infra = *infraStatus

	if conf.Spec.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		o, err := bootstrapOVN(conf, client, infraStatus)
		if err != nil {
			return nil, err
		}
		out.OVN = *o
	}

	out.IPTablesAlerter = iptablesAlerterBootstrap(client.ClientFor("").CRClient())

	out.TLSProfile, err = getTLSProfile(client)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func getTLSProfile(client cnoclient.Client) (bootstrap.TLSProfile, error) {
	apiServer := &configv1.APIServer{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: openshifttls.APIServerName}, apiServer); err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to fetch apiserver.config.openshift.io/%s: %w", openshifttls.APIServerName, err)
	}

	profileSpec, err := openshifttls.GetTLSProfileSpec(apiServer.Spec.TLSSecurityProfile)
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to get TLS profile spec: %w", err)
	}

	return bootstrap.TLSProfile{Spec: profileSpec, Adherence: apiServer.Spec.TLSAdherence}, nil
}

func iptablesAlerterBootstrap(cl crclient.Reader) bootstrap.IPTablesAlerterBootstrapResult {
	result := bootstrap.IPTablesAlerterBootstrapResult{
		Enabled: true,
	}

	cm := &corev1.ConfigMap{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: "openshift-network-operator",
		Name:      "iptables-alerter-config",
	}, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Error fetching iptables-alerter-config configmap: %v", err)
		}
		return result
	}

	enabled := cm.Data["enabled"]
	if enabled == "false" {
		result.Enabled = false
	} else if enabled != "true" {
		klog.Warningf("Ignoring unexpected iptables-alerter-config value enabled=%q", enabled)
	}

	return result
}
