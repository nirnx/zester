package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// fakeObjectStore is an in-memory implementation of jetstream.ObjectStore for testing.
type fakeObjectStore struct {
	mu      sync.RWMutex
	objects map[string]fakeObject
}

type fakeObject struct {
	data []byte
	info *jetstream.ObjectInfo
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string]fakeObject)}
}

func (f *fakeObjectStore) Put(_ context.Context, obj jetstream.ObjectMeta, reader io.Reader) (*jetstream.ObjectInfo, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	info := &jetstream.ObjectInfo{
		ObjectMeta: obj,
		Bucket:     "test",
		Size:       uint64(len(data)),
		ModTime:    time.Now(),
	}
	f.mu.Lock()
	f.objects[obj.Name] = fakeObject{data: data, info: info}
	f.mu.Unlock()
	return info, nil
}

func (f *fakeObjectStore) Get(_ context.Context, name string, _ ...jetstream.GetObjectOpt) (jetstream.ObjectResult, error) {
	f.mu.RLock()
	obj, ok := f.objects[name]
	f.mu.RUnlock()
	if !ok {
		return nil, jetstream.ErrObjectNotFound
	}
	return &fakeObjectResult{
		Reader: bytes.NewReader(obj.data),
		info:   obj.info,
	}, nil
}

func (f *fakeObjectStore) GetInfo(_ context.Context, name string, _ ...jetstream.GetObjectInfoOpt) (*jetstream.ObjectInfo, error) {
	f.mu.RLock()
	obj, ok := f.objects[name]
	f.mu.RUnlock()
	if !ok {
		return nil, jetstream.ErrObjectNotFound
	}
	return obj.info, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objects[name]; !ok {
		return jetstream.ErrObjectNotFound
	}
	delete(f.objects, name)
	return nil
}

func (f *fakeObjectStore) List(_ context.Context, _ ...jetstream.ListObjectsOpt) ([]*jetstream.ObjectInfo, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.objects) == 0 {
		return nil, jetstream.ErrNoObjectsFound
	}
	var infos []*jetstream.ObjectInfo
	for _, obj := range f.objects {
		infos = append(infos, obj.info)
	}
	return infos, nil
}

// Unused interface methods — panic if called unexpectedly in tests.

func (f *fakeObjectStore) PutBytes(_ context.Context, name string, data []byte) (*jetstream.ObjectInfo, error) {
	return f.Put(context.Background(), jetstream.ObjectMeta{Name: name}, bytes.NewReader(data))
}
func (f *fakeObjectStore) PutString(context.Context, string, string) (*jetstream.ObjectInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) PutFile(context.Context, string) (*jetstream.ObjectInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) GetBytes(_ context.Context, name string, opts ...jetstream.GetObjectOpt) ([]byte, error) {
	result, err := f.Get(context.Background(), name, opts...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	return io.ReadAll(result)
}
func (f *fakeObjectStore) GetString(context.Context, string, ...jetstream.GetObjectOpt) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) GetFile(context.Context, string, string, ...jetstream.GetObjectOpt) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) UpdateMeta(context.Context, string, jetstream.ObjectMeta) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) AddLink(context.Context, string, *jetstream.ObjectInfo) (*jetstream.ObjectInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) AddBucketLink(context.Context, string, jetstream.ObjectStore) (*jetstream.ObjectInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) Seal(context.Context) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) Watch(context.Context, ...jetstream.WatchOpt) (jetstream.ObjectWatcher, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeObjectStore) Status(context.Context) (jetstream.ObjectStoreStatus, error) {
	return nil, fmt.Errorf("not implemented")
}

// fakeObjectResult implements jetstream.ObjectResult.
type fakeObjectResult struct {
	*bytes.Reader
	info *jetstream.ObjectInfo
}

func (r *fakeObjectResult) Close() error                         { return nil }
func (r *fakeObjectResult) Info() (*jetstream.ObjectInfo, error) { return r.info, nil }
func (r *fakeObjectResult) Error() error                         { return nil }
