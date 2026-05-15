package operconfig_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func testFailures() {
	t := newTestDriver()

	var watcher *watch.FakeWatcher

	JustBeforeEach(func() {
		watcher = t.fakeCache.AwaitWatcher(&operv1.Network{})
	})

	When("merging the cluster config into the operator config fails", func() {
		JustBeforeEach(func() {
			t.setupDynamicClientFailOnAction("patch", "networks")
			watcher.Add(t.operConfig)
		})

		t.testDegradedStatusCondition()
	})

	When("the operator config fails validation", func() {
		BeforeEach(func() {
			// Empty ServiceNetwork will cause validation to fail.
			t.clusterConfig.Spec.ServiceNetwork = []string{}
		})

		JustBeforeEach(func() {
			watcher.Add(t.operConfig)
		})

		t.testDegradedStatusCondition()
	})

	When("applying rendered resources fails", func() {
		JustBeforeEach(func() {
			t.setupDynamicClientFailOnAction("patch", "daemonsets")
			watcher.Add(t.operConfig)
		})

		t.testDegradedStatusCondition()
	})

	When("rendering fails due to missing console plugin image env var", func() {
		JustBeforeEach(func(ctx context.Context) {
			// Create the ConsolePlugin CRD so console plugin rendering is attempted
			Expect(t.fakeClient.Default().CRClient().Create(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "consoleplugins.console.openshift.io",
				},
			})).To(Succeed())

			watcher.Add(t.operConfig)
		})

		t.testDegradedStatusCondition()
	})
}
