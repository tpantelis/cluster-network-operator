package fake

import (
	"context"
	"encoding/json"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	faketyped "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	osoperclient "github.com/openshift/client-go/operator/clientset/versioned"
	osoperfakeclient "github.com/openshift/client-go/operator/clientset/versioned/fake"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type FakeClient struct {
	clusterClients map[string]*FakeClusterClient
}

type FakeClusterClient struct {
	// dynclient is an untyped, uncached client for making direct requests
	// against the apiserver.
	dynclient dynamic.Interface

	// kClient is an fake kubernetes client for kubernetes objects
	kClient kubernetes.Interface

	// crclient is the controller-runtime ClusterClient, for controllers that have
	// not yet been migrated.
	crclient crclient.Client

	osOperClient osoperclient.Interface

	restMapper meta.RESTMapper

	// customInformers stores custom informers added via AddCustomInformer
	customInformers []cache.SharedInformer
}

func (fc *FakeClient) ClientFor(name string) cnoclient.ClusterClient {
	if len(name) == 0 {
		return fc.Default()
	}
	return fc.clusterClients[name]
}

func (fc *FakeClient) Default() cnoclient.ClusterClient {
	return fc.ClientFor(names.DefaultClusterName)
}

func (fc *FakeClient) Start(ctx context.Context) error {
	// Start custom informers for all cluster clients
	for _, clusterClient := range fc.clusterClients {
		for _, inf := range clusterClient.customInformers {
			go inf.Run(ctx.Done())
		}
	}
	return nil
}

func (fc *FakeClient) Clients() map[string]cnoclient.ClusterClient {
	out := make(map[string]cnoclient.ClusterClient)
	for k, v := range fc.clusterClients {
		out[k] = v
	}
	return out
}

// isKubernetesObject returns true if the object is a core Kubernetes type
// that can be handled by the fake Kubernetes typed client.
func isKubernetesObject(obj crclient.Object) bool {
	kKind, _, _ := scheme.Scheme.ObjectKinds(obj)
	for _, v := range kKind {
		// Include core Kubernetes API groups
		if v.Group == "" { // core group (pods, services, etc)
			return true
		}
		// Include k8s.io groups, but exclude extension APIs that aren't part of the typed client
		if strings.HasSuffix(v.Group, ".k8s.io") &&
			v.Group != "apiextensions.k8s.io" && // CRDs are not in typed client
			!strings.HasPrefix(v.Group, "sigs.k8s.io") { // SIG projects use dynamic client
			return true
		}
	}
	return false
}

// applyPatchReactor intercepts Patch operations with ApplyPatchType (Server-Side Apply)
// and creates the object if it doesn't exist, mimicking real Server-Side Apply behavior.
func applyPatchReactor(dynClient *fakedynamic.FakeDynamicClient) func(action testing.Action) (handled bool, ret runtime.Object, err error) {
	return func(action testing.Action) (handled bool, ret runtime.Object, err error) {
		patchAction, ok := action.(testing.PatchAction)
		if !ok {
			return false, nil, nil
		}

		// Only intercept ApplyPatchType (Server-Side Apply)
		if patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil // Let the default reactor handle it
		}

		// Get object info
		gvr := action.GetResource()
		namespace := action.GetNamespace()
		name := patchAction.GetName()
		patchData := patchAction.GetPatch()

		// Decode the patch data into a map
		var data map[string]interface{}
		if err := json.Unmarshal(patchData, &data); err != nil {
			return true, nil, err
		}

		// Create an Unstructured object from the decoded data
		unstructuredObj := &unstructured.Unstructured{Object: data}

		// Ensure name and namespace are set correctly
		unstructuredObj.SetName(name)
		if namespace != "" {
			unstructuredObj.SetNamespace(namespace)
		}

		// Set the GVK
		kind := unstructuredObj.GetKind()
		if kind == "" {
			// If kind is not set in the patch data, we need to derive it from the resource
			// This is a simplified approach - in real Server-Side Apply, the kind would be in the patch
			return false, nil, nil // Let the default reactor handle it
		}
		gvk := gvr.GroupVersion().WithKind(kind)
		unstructuredObj.SetGroupVersionKind(gvk)

		// Try to get the existing object
		obj, err := dynClient.Tracker().Get(gvr, namespace, name)
		if err == nil && obj != nil {
			// Object exists, update it by replacing with the patch data
			// For Server-Side Apply, we just replace the whole object
			err = dynClient.Tracker().Update(gvr, unstructuredObj, namespace)
			if err != nil {
				return true, nil, err
			}
			return true, unstructuredObj, nil
		}

		// Object doesn't exist, create it
		err = dynClient.Tracker().Create(gvr, unstructuredObj, namespace)
		if err != nil {
			return true, nil, err
		}

		// Return the object we created
		return true, unstructuredObj, nil
	}
}

// NewFakeClient creates a fake client with a backing store that contains the given objects.
//
// Note that, due to limitations in the test infrastructure, each client has an independent store.
// This means that changes made in, say, the crclient, won't show up in the Dynamic client or the typed
// Kubernetes client
// TODO: stop using the crclient entirely
// TODO: Somehow convince upstream client-go to allow sharing the store between the dynamic and typed clients.
//
//	(this is't that big a deal since we don't actually use the typed client that much).
func NewFakeClient(objs ...crclient.Object) cnoclient.Client {
	// silly go type conversion
	oo := make([]runtime.Object, 0, len(objs))
	k8sObjs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		oo = append(oo, o)
		// Only include core Kubernetes objects in the typed Kubernetes client
		if isKubernetesObject(o) {
			k8sObjs = append(k8sObjs, o)
		}
	}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: ""}}

	// Extract all registered GroupVersions from the scheme for the REST mapper
	restMapper := meta.NewDefaultRESTMapper(scheme.Scheme.PrioritizedVersionsAllGroups())
	for gvk, _ := range scheme.Scheme.AllKnownTypes() {
		restMapper.Add(gvk, meta.RESTScopeNamespace)
	}

	dynClient := fakedynamic.NewSimpleDynamicClient(scheme.Scheme, oo...)
	// Add reactor to handle Server-Side Apply (ApplyPatchType) by creating objects if they don't exist
	dynClient.PrependReactor("patch", "*", applyPatchReactor(dynClient))

	fc := FakeClusterClient{
		kClient:      faketyped.NewClientset(k8sObjs...),
		dynclient:    dynClient,
		crclient:     crfake.NewClientBuilder().WithStatusSubresource(co).WithObjects(objs...).Build(),
		osOperClient: osoperfakeclient.NewClientset(),
		restMapper:   restMapper,
	}
	return &FakeClient{
		clusterClients: map[string]*FakeClusterClient{
			names.DefaultClusterName: &fc,
		},
	}
}

func (fc *FakeClusterClient) Kubernetes() kubernetes.Interface {
	return fc.kClient
}

func (fc *FakeClusterClient) OpenshiftOperatorClient() osoperclient.Interface {
	return fc.osOperClient
}

func (fc *FakeClusterClient) Config() *rest.Config {
	return nil
}

func (fc *FakeClusterClient) Dynamic() dynamic.Interface {
	return fc.dynclient
}

func (fc *FakeClusterClient) CRClient() crclient.Client {
	return fc.crclient
}

func (fc *FakeClusterClient) RESTMapper() meta.RESTMapper {
	return fc.restMapper
}

func (fc *FakeClusterClient) Scheme() *runtime.Scheme {
	return scheme.Scheme
}
func (fc *FakeClusterClient) OperatorHelperClient() operatorv1helpers.OperatorClient {
	panic("not implemented!")
}

func (fc *FakeClusterClient) HostPort() (string, string) {
	return "testing", "9999"
}

func (fc *FakeClusterClient) AddCustomInformer(inf cache.SharedInformer) {
	fc.customInformers = append(fc.customInformers, inf)
}
