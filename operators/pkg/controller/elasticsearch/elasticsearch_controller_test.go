// +build integration

package elasticsearch

import (
	"fmt"
	"testing"

	"github.com/elastic/k8s-operators/operators/pkg/apis/elasticsearch/v1alpha1"
	elasticsearchv1alpha1 "github.com/elastic/k8s-operators/operators/pkg/apis/elasticsearch/v1alpha1"
	"github.com/elastic/k8s-operators/operators/pkg/controller/common/operator"
	"github.com/elastic/k8s-operators/operators/pkg/utils/k8s"
	"github.com/elastic/k8s-operators/operators/pkg/utils/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var c k8s.Client

var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: "default"}}
var discoveryServiceKey = types.NamespacedName{Name: "foo-es-discovery", Namespace: "default"}
var publicServiceKey = types.NamespacedName{Name: "foo-es-public", Namespace: "default"}

func getESPods(t *testing.T) []corev1.Pod {
	esPods := &corev1.PodList{}
	esPodSelector := client.ListOptions{Namespace: "default"}
	err := esPodSelector.SetLabelSelector("common.k8s.elastic.co/type=elasticsearch")
	assert.NoError(t, err)
	test.RetryUntilSuccess(t, func() error {
		return c.List(&esPodSelector, esPods)
	})
	return esPods.Items
}

func TestReconcile(t *testing.T) {
	instance := &elasticsearchv1alpha1.ElasticsearchCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "default"},
		Spec: elasticsearchv1alpha1.ElasticsearchSpec{
			Version:          "7.0.0",
			SetVMMaxMapCount: false,
			Topologies: []v1alpha1.ElasticsearchTopologySpec{
				{
					NodeCount: 3,
				},
			},
		},
	}

	// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
	// channel when it is finished.
	mgr, err := manager.New(test.Config, manager.Options{})

	assert.NoError(t, err)
	c = k8s.WrapClient(mgr.GetClient())

	r, err := newReconciler(mgr, operator.Parameters{OperatorImage: "operator-image-dummy"})
	require.NoError(t, err)
	recFn, requests := SetupTestReconcile(r)
	controller, err := add(mgr, recFn)
	assert.NoError(t, err)
	assert.NoError(t, addWatches(controller, r))

	stopMgr, mgrStopped := StartTestManager(mgr, t)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	// Pre-create dependent Endpoint which will not be created automatically as only the Elasticsearch controller is running.
	endpoints := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "foo-es-public", Namespace: "default"}}
	err = c.Create(endpoints)
	assert.NoError(t, err)
	// Create the Elasticsearch object and expect the Reconcile and Deployment to be created
	err = c.Create(instance)
	// The instance object may not be a valid object because it might be missing some required fields.
	// Please modify the instance object by adding required fields and then remove the following if statement.
	if apierrors.IsInvalid(err) {
		t.Logf("failed to create object, got an invalid object error: %v", err)
		return
	}
	assert.NoError(t, err)
	defer c.Delete(instance)

	test.CheckReconcileCalled(t, requests, expectedRequest)

	// Elasticsearch pods should be created
	esPods := getESPods(t)
	assert.Equal(t, 3, len(esPods))

	// Services should be created
	discoveryService := &corev1.Service{}
	test.RetryUntilSuccess(t, func() error { return c.Get(discoveryServiceKey, discoveryService) })
	publicService := &corev1.Service{}
	test.RetryUntilSuccess(t, func() error { return c.Get(publicServiceKey, publicService) })

	// Delete resources and expect Reconcile to be called and eventually recreate them
	// ES pod
	assert.NoError(t, c.Delete(&esPods[0]))
	test.CheckReconcileCalled(t, requests, expectedRequest)
	test.RetryUntilSuccess(t, func() error {
		nPods := len(getESPods(t))
		if nPods != 3 {
			return fmt.Errorf("Got %d pods out of 3", nPods)
		}
		return nil
	})

	// Services
	test.CheckResourceDeletionTriggersReconcile(t, c, requests, publicServiceKey, publicService, expectedRequest)
	test.CheckResourceDeletionTriggersReconcile(t, c, requests, discoveryServiceKey, discoveryService, expectedRequest)

	// Manually delete Deployment and Services since GC might not be enabled in the test control plane
	test.DeleteIfExists(t, c, publicService)
	test.DeleteIfExists(t, c, discoveryService)
	test.DeleteIfExists(t, c, endpoints)

}