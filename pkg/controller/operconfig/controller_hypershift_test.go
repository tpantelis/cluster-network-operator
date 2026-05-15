/*
SPDX-License-Identifier: Apache-2.0

Copyright Contributors to the Submariner project.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operconfig_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"
	cohelpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func testHypershiftMode() {
	t := newTestDriver()

	const (
		hostedClusterName      = "my-hosted-cluster"
		hostedClusterNamespace = "my-hosted-cluster-ns"
		infraName              = "my-hosted-cluster-infra"
	)

	BeforeEach(func() {
		// Set HyperShift environment variables
		Expect(os.Setenv("HYPERSHIFT", "true")).To(Succeed())
		Expect(os.Setenv("HOSTED_CLUSTER_NAME", hostedClusterName)).To(Succeed())
		Expect(os.Setenv("HOSTED_CLUSTER_NAMESPACE", hostedClusterNamespace)).To(Succeed())
		Expect(os.Setenv("NETWORKING_CONSOLE_PLUGIN_IMAGE", "quay.io/openshift/networking-console-plugin:latest")).To(Succeed())

		DeferCleanup(func() {
			os.Unsetenv("HYPERSHIFT")
			os.Unsetenv("HOSTED_CLUSTER_NAME")
			os.Unsetenv("HOSTED_CLUSTER_NAMESPACE")
			os.Unsetenv("NETWORKING_CONSOLE_PLUGIN_IMAGE")
		})

		// Set infrastructure name for HyperShift mode
		t.infrastructure.Status.InfrastructureName = infraName
	})

	JustBeforeEach(func(ctx context.Context) {
		// Create HostedControlPlane object in management cluster
		hcp := &uns.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "hypershift.openshift.io/v1beta1",
				"kind":       "HostedControlPlane",
				"metadata": map[string]interface{}{
					"name":      hostedClusterName,
					"namespace": hostedClusterNamespace,
				},
				"spec": map[string]interface{}{
					"clusterID":                    infraName,
					"controllerAvailabilityPolicy": "SingleReplica",
					"networking": map[string]interface{}{
						"apiServer": map[string]interface{}{
							"advertiseAddress": "172.20.0.1",
							"port":             int64(6443),
						},
						"serviceNetwork": []interface{}{
							map[string]interface{}{
								"cidr": "172.30.0.0/16",
							},
						},
					},
				},
				"status": map[string]interface{}{},
			},
		}
		Expect(t.fakeClient.ClientFor(names.ManagementClusterName).CRClient().Create(ctx, hcp)).To(Succeed())

		// Create ovn-cert secret in hosted cluster that will be copied to management cluster
		ovnCertSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn-cert",
				Namespace: "openshift-ovn-kubernetes",
			},
			Data: map[string][]byte{
				"tls.crt": []byte(testCertificate),
				"tls.key": []byte("test-key"),
			},
		}
		Expect(t.fakeClient.Default().CRClient().Create(ctx, ovnCertSecret)).To(Succeed())

		// Create ovn-ca ConfigMap in hosted cluster that will be copied to management cluster
		ovnCAConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn-ca",
				Namespace: "openshift-ovn-kubernetes",
			},
			Data: map[string]string{
				"ca-bundle.crt": testCertificate,
			},
		}
		Expect(t.fakeClient.Default().CRClient().Create(ctx, ovnCAConfigMap)).To(Succeed())

		// Create service CA ConfigMap in management cluster namespace
		serviceCAConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-service-ca.crt",
				Namespace: hostedClusterNamespace,
			},
			Data: map[string]string{
				"service-ca.crt": testCertificate,
			},
		}
		Expect(t.fakeClient.ClientFor(names.ManagementClusterName).CRClient().Create(ctx, serviceCAConfigMap)).To(Succeed())

		t.fakeCache.AwaitWatcher(&operv1.Network{}).Add(t.operConfig)
	})

	Specify("reconciliation should correctly apply resources", func(ctx context.Context) {
		t.awaitClusterOperatorConditions(ctx, func(g Gomega, conditions []configv1.ClusterOperatorStatusCondition) {
			g.Expect(cohelpers.IsStatusConditionTrue(conditions, configv1.OperatorAvailable)).To(BeTrue())
			g.Expect(cohelpers.IsStatusConditionFalse(conditions, configv1.OperatorProgressing)).To(BeTrue())
			g.Expect(cohelpers.IsStatusConditionFalse(conditions, configv1.OperatorDegraded)).To(BeTrue())
		})

		Eventually(func(g Gomega) {
			// Verify resources were copied to management cluster.
			_, err := t.fakeClient.ClientFor(names.ManagementClusterName).Dynamic().
				Resource(corev1.SchemeGroupVersion.WithResource("secrets")).
				Namespace(hostedClusterNamespace).Get(ctx, "ovn-cert", metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred(), "ovn-cert secret should be copied to management cluster")

			_, err = t.fakeClient.ClientFor(names.ManagementClusterName).Dynamic().
				Resource(corev1.SchemeGroupVersion.WithResource("configmaps")).
				Namespace(hostedClusterNamespace).Get(ctx, "ovn-ca", metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred(), "ovn-ca configmap should be copied to management cluster")

			// Verify resources have GenerateStatusLabel set to infrastructure name, not "standalone"
			obj, err := t.fakeClient.Default().Dynamic().Resource(appsv1.SchemeGroupVersion.WithResource("daemonsets")).
				Namespace(util.OVN_NAMESPACE).Get(ctx, "ovnkube-node", metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(obj.GetLabels()).To(HaveKeyWithValue(names.GenerateStatusLabel, infraName))

			// Verify owner references are still set correctly
			g.Expect(obj.GetOwnerReferences()).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
				"APIVersion": Equal(operv1.GroupVersion.String()),
				"Kind":       Equal("Network"),
				"Name":       Equal(t.operConfig.Name),
			})))

			// In HyperShift mode, management cluster resources (with cluster-name annotation)
			// are tracked separately via the RelatedClusterObjectsAnnotation
			co := &configv1.ClusterOperator{}
			g.Expect(t.fakeClient.Default().CRClient().Get(ctx, types.NamespacedName{Name: clusterOperatorName}, co)).To(Succeed())

			// The annotation should exist and contain cluster name references
			annotation, exists := co.Annotations[names.RelatedClusterObjectsAnnotation]
			g.Expect(exists).To(BeTrue(), "RelatedClusterObjectsAnnotation should be present")
			g.Expect(annotation).NotTo(BeEmpty(), "RelatedClusterObjectsAnnotation should contain management cluster resources")
		}).Within(5 * time.Second).Should(Succeed())
	})
}

const testCertificate = `-----BEGIN CERTIFICATE-----
MIIC/zCCAeegAwIBAgIUHSjDOFf72A3VMGMD8N13zO8KQR4wDQYJKoZIhvcNAQEL
BQAwDzENMAsGA1UEAwwEdGVzdDAeFw0yNjA1MTgxNTIwNDVaFw0yNzA1MTgxNTIw
NDVaMA8xDTALBgNVBAMMBHRlc3QwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEK
AoIBAQCTbJAURvth/OnL8xQBQMX+PDb1sRWY9YX5Zj8MxUsURl3gFtbG1WY9P69Q
Q0aNkRIvROHwiYcbDdhNde1c+EK3hPyUCy1GUxHVpn8jszVypDbrhBXEpjiia/E2
R9R2zNxJ4by76EPUVFYHjHojikdsqIb9AN+4r/vGkmZzmDU2F+tStztkkSDzx6ic
KqajOVeUaYdYjIOx/YjZ74lxMkNjGdHFrI6lTRnGxrnPt6slZJ6vzHTp6vOm6E5h
5PNNtQ9Dlh6nYIDpXin6xYiz/sVjmQwvbSF8uF31PdCdOyIMir/E9eKnU0pNy93t
Wy5JWpBkQYchGFHysyQJyxJ5BeRvAgMBAAGjUzBRMB0GA1UdDgQWBBSESpzdW5Rd
9kfnzGypi6YKXY3uYjAfBgNVHSMEGDAWgBSESpzdW5Rd9kfnzGypi6YKXY3uYjAP
BgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQAToais8sLtsVHF0jGD
U8jyin4ecQlorW3hz3lkutPZsNGHet6wJ5BIMuEplWaQQmMe75U6fS4H+82Rf3sb
3qKu5S/WgvL3hPRt1W/83zqEzmAH8tG8PscUIjLUM+sEeij0I0RIFdGaJ85pAQLk
6+r2QhqN/JEH4EbPrukcq129UPo1dwZe8LNT6rqYnDcMwESP0etqce0FvUBBjKm6
hXjod7K3U4j++cmMG8HEa3Q4dy35PTdmsE7TcEjIj+/a947DmFYwCoKjTpwqoOZa
xnLKidXWdKQcct92WizUVv15nAp/rLXC8dhFKoRKCtoAeKvKJPNUlVVlOgS3fbrg
petp
-----END CERTIFICATE-----
`
