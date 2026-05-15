package fake

import (
	"context"
	"reflect"
	"sync"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"
	crCache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type Cache struct {
	sync.RWMutex
	client    crclient.Client
	informers map[reflect.Type]toolscache.SharedIndexInformer
	watchers  map[reflect.Type]*watch.FakeWatcher
}

func NewCache(client crclient.Client) *Cache {
	return &Cache{
		informers: make(map[reflect.Type]toolscache.SharedIndexInformer),
		client:    client,
		watchers:  make(map[reflect.Type]*watch.FakeWatcher),
	}
}

func (f *Cache) Get(ctx context.Context, key crclient.ObjectKey, obj crclient.Object, opts ...crclient.GetOption) error {
	return f.client.Get(ctx, key, obj, opts...)
}

func (f *Cache) List(ctx context.Context, list crclient.ObjectList, opts ...crclient.ListOption) error {
	return f.client.List(ctx, list, opts...)
}

func (f *Cache) GetInformer(ctx context.Context, obj crclient.Object, _ ...crCache.InformerGetOption) (crCache.Informer, error) {
	objType := reflect.TypeOf(obj)

	f.Lock()
	if informer, ok := f.informers[objType]; ok {
		f.Unlock()
		return informer, nil
	}

	fakeWatcher := watch.NewFake()

	newInformer := toolscache.NewSharedIndexInformer(
		&toolscache.ListWatch{
			// ListWithContextFunc is not called when the reflector uses watch-list (SendInitialEvents: true)
			// which is the default behavior in newer Kubernetes versions
			WatchFuncWithContext: func(watchCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				// Send a bookmark event after the watch is established
				// This signals to the reflector that the initial list/watch sync is complete
				go func() {
					defer func() {
						// Recover from panic if the watcher channel is closed
						_ = recover()
					}()

					select {
					case <-time.After(10 * time.Millisecond):
						// Small delay to ensure watch handler is ready
						bookmark := obj.DeepCopyObject().(crclient.Object)
						bookmark.SetResourceVersion("1")
						// Set the annotation that marks this as the initial events end bookmark
						bookmark.SetAnnotations(map[string]string{
							metav1.InitialEventsAnnotationKey: "true",
						})
						fakeWatcher.Action(watch.Bookmark, bookmark)
					case <-watchCtx.Done():
						// Context canceled, don't send bookmark
						return
					}
				}()
				return fakeWatcher, nil
			},
		},
		obj,
		0,
		toolscache.Indexers{},
	)

	// Add to map BEFORE starting the informer to avoid race condition
	f.watchers[objType] = fakeWatcher
	f.informers[objType] = newInformer
	f.Unlock()

	go func() {
		newInformer.Run(ctx.Done())
	}()

	return newInformer, nil
}

func (f *Cache) GetInformerForKind(_ context.Context, _ schema.GroupVersionKind, _ ...crCache.InformerGetOption) (crCache.Informer, error) {
	return nil, nil
}

func (f *Cache) RemoveInformer(_ context.Context, _ crclient.Object) error {
	return nil
}

func (f *Cache) Start(_ context.Context) error {
	return nil
}

func (f *Cache) WaitForCacheSync(ctx context.Context) bool {
	f.RLock()
	informersCopy := make([]toolscache.SharedIndexInformer, 0, len(f.informers))
	for _, informer := range f.informers {
		informersCopy = append(informersCopy, informer)
	}
	f.RUnlock()

	for _, informer := range informersCopy {
		if !toolscache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
			return false
		}
	}

	return true
}

func (f *Cache) IndexField(ctx context.Context, obj crclient.Object, field string, extractValue crclient.IndexerFunc) error {
	return nil
}

// AwaitWatcher waits for a watcher of the given type to be created
func (f *Cache) AwaitWatcher(obj crclient.Object) *watch.FakeWatcher {
	objType := reflect.TypeOf(obj)

	var watcher *watch.FakeWatcher

	Eventually(func(g Gomega) {
		f.RLock()
		defer f.RUnlock()
		watcher = f.watchers[objType]
		g.Expect(watcher).NotTo(BeNil(), "watcher of type %T was not created", obj)
	}).Within(5 * time.Second).ProbeEvery(50 * time.Millisecond).Should(Succeed())

	return watcher
}

// SeedInformerStore adds an object directly to the informer's store without triggering watch events.
// This allows subsequent Modify events to be treated as updates rather than creates.
func (f *Cache) SeedInformerStore(obj crclient.Object) {
	objType := reflect.TypeOf(obj)

	f.RLock()
	informer, ok := f.informers[objType]
	f.RUnlock()

	Expect(ok).To(BeTrue(), "no informer exists for type %T", obj)
	Expect(informer.GetStore().Add(obj)).To(Succeed())
}
