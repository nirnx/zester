package bus_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// testJS implements bus.JetStreamAPI for testing. It delegates KV operations
// to bustest.FakeKV and provides minimal stream stubs. We inline this here
// because bustest.FakeJS is currently not exported due to an interface
// satisfaction issue with fakeStream.
type testJS struct {
	mu            sync.RWMutex
	buckets       map[string]*bustest.FakeKV
	streams       map[string]jetstream.Stream
	kvConfigs     map[string]jetstream.KeyValueConfig
	streamConfigs map[string]jetstream.StreamConfig
}

func newTestJS() *testJS {
	return &testJS{
		buckets:       make(map[string]*bustest.FakeKV),
		streams:       make(map[string]jetstream.Stream),
		kvConfigs:     make(map[string]jetstream.KeyValueConfig),
		streamConfigs: make(map[string]jetstream.StreamConfig),
	}
}

func (j *testJS) CreateOrUpdateKeyValue(_ context.Context, cfg jetstream.KeyValueConfig) (bus.KV, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.kvConfigs[cfg.Bucket] = cfg
	if existing, ok := j.buckets[cfg.Bucket]; ok {
		return existing, nil
	}

	kv := bustest.NewFakeKV(cfg.Bucket, cfg.TTL)
	j.buckets[cfg.Bucket] = kv
	return kv, nil
}

// kvConfig returns the last KeyValueConfig passed to CreateOrUpdateKeyValue
// for the given bucket, for replica-count assertions.
func (j *testJS) kvConfig(bucket string) jetstream.KeyValueConfig {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.kvConfigs[bucket]
}

// streamConfig returns the last StreamConfig passed to CreateStream for the
// given stream.
func (j *testJS) streamConfig(name string) jetstream.StreamConfig {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.streamConfigs[name]
}

func (j *testJS) KeyValue(_ context.Context, bucket string) (bus.KV, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	kv, ok := j.buckets[bucket]
	if !ok {
		return nil, fmt.Errorf("bucket not found: %s", bucket)
	}
	return kv, nil
}

func (j *testJS) DeleteKeyValue(_ context.Context, bucket string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if _, ok := j.buckets[bucket]; !ok {
		return fmt.Errorf("bucket not found: %s", bucket)
	}
	delete(j.buckets, bucket)
	return nil
}

func (j *testJS) CreateStream(_ context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.streamConfigs[cfg.Name] = cfg
	s := &testStream{name: cfg.Name, cfg: cfg}
	j.streams[cfg.Name] = s
	return s, nil
}

func (j *testJS) Publish(_ context.Context, _ string, _ []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return &jetstream.PubAck{Stream: "fake"}, nil
}

// testStream is a minimal jetstream.Stream stub for tests.
type testStream struct {
	name string
	cfg  jetstream.StreamConfig
}

func (s *testStream) Info(_ context.Context, _ ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return &jetstream.StreamInfo{Config: s.cfg}, nil
}

func (s *testStream) CachedInfo() *jetstream.StreamInfo {
	return &jetstream.StreamInfo{Config: s.cfg}
}

func (s *testStream) Purge(_ context.Context, _ ...jetstream.StreamPurgeOpt) error { return nil }

func (s *testStream) GetMsg(_ context.Context, _ uint64, _ ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) GetLastMsgForSubject(_ context.Context, _ string) (*jetstream.RawStreamMsg, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) DeleteMsg(_ context.Context, _ uint64) error       { return nil }
func (s *testStream) SecureDeleteMsg(_ context.Context, _ uint64) error { return nil }

func (s *testStream) CreateOrUpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) CreateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) UpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) ResetConsumer(_ context.Context, _ string) (*jetstream.ConsumerResetResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) ResetConsumerToSequence(_ context.Context, _ string, _ uint64) (*jetstream.ConsumerResetResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) OrderedConsumer(_ context.Context, _ jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) Consumer(_ context.Context, _ string) (jetstream.Consumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) DeleteConsumer(_ context.Context, _ string) error { return nil }

func (s *testStream) PauseConsumer(_ context.Context, _ string, _ time.Time) (*jetstream.ConsumerPauseResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) ResumeConsumer(_ context.Context, _ string) (*jetstream.ConsumerPauseResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) ListConsumers(_ context.Context) jetstream.ConsumerInfoLister { return nil }
func (s *testStream) ConsumerNames(_ context.Context) jetstream.ConsumerNameLister { return nil }
func (s *testStream) UnpinConsumer(_ context.Context, _ string, _ string) error    { return nil }

func (s *testStream) CreateOrUpdatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) CreatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) UpdatePushConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *testStream) PushConsumer(_ context.Context, _ string) (jetstream.PushConsumer, error) {
	return nil, fmt.Errorf("not implemented")
}

// --- Subject Tests ---

func TestSubjectConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"prefix", bus.SubjectPrefix, "zester"},
		{"cmd", bus.SubjectCmd, "zester.cmd"},
		{"event", bus.SubjectEvent, "zester.event"},
		{"fact", bus.SubjectFact, "zester.fact"},
		{"basket", bus.SubjectBasket, "zester.basket"},
		{"job", bus.SubjectJob, "zester.job"},
		{"reactor", bus.SubjectReactor, "zester.reactor"},
		{"target_resolve", bus.SubjectTargetResolve, "zester.target.resolve"},
		{"admin_enroll_approve", bus.SubjectAdminEnrollApprove, "zester.admin.enroll.approve"},
		{"admin_enroll_reject", bus.SubjectAdminEnrollReject, "zester.admin.enroll.reject"},
		{"admin_enroll_revoke", bus.SubjectAdminEnrollRevoke, "zester.admin.enroll.revoke"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

func TestSubjectHelpers(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"cmd_subject", bus.CmdSubject("web-01"), "zester.cmd.web-01"},
		{"cmd_all", bus.CmdSubjectAll(), "zester.cmd.*"},
		{"event_subject", bus.EventSubject("web-01"), "zester.event.web-01"},
		{"event_all", bus.EventSubjectAll(), "zester.event.>"},
		{"beacon", bus.BeaconSubject("web-01", "disk"), "zester.event.web-01.beacon.disk"},
		{"fact", bus.FactSubject("web-01"), "zester.fact.web-01"},
		{"basket", bus.BasketSubject("web-01", "network.ip_addrs"), "zester.basket.web-01.network.ip_addrs"},
		{"job", bus.JobSubject("abc123"), "zester.job.abc123"},
		{"job_dispatch", bus.JobDispatchSubject("abc123"), "zester.job.abc123.dispatch"},
		{"job_ack", bus.JobAckSubject("abc123", "web-01"), "zester.job.abc123.ack.web-01"},
		{"job_return", bus.JobReturnSubject("abc123", "web-01"), "zester.job.abc123.return.web-01"},
		{"job_status", bus.JobStatusSubject("abc123"), "zester.job.abc123.status"},
		{"job_cancel", bus.JobCancelSubject("abc123"), "zester.job.abc123.cancel"},
		{"job_all", bus.JobAllSubject("abc123"), "zester.job.abc123.>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

// --- Codec Tests ---

func TestEncodeDecodeRoundtrip(t *testing.T) {
	type testPayload struct {
		Name  string   `msgpack:"name"`
		Count int      `msgpack:"count"`
		Tags  []string `msgpack:"tags"`
	}

	original := testPayload{
		Name:  "web-01",
		Count: 42,
		Tags:  []string{"production", "us-east-1"},
	}

	data, err := bus.Encode(original)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("encoded data is empty")
	}

	var decoded testPayload
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.Name != original.Name {
		t.Errorf("name: got %q, want %q", decoded.Name, original.Name)
	}
	if decoded.Count != original.Count {
		t.Errorf("count: got %d, want %d", decoded.Count, original.Count)
	}
	if len(decoded.Tags) != len(original.Tags) {
		t.Fatalf("tags length: got %d, want %d", len(decoded.Tags), len(original.Tags))
	}
	for i, tag := range decoded.Tags {
		if tag != original.Tags[i] {
			t.Errorf("tags[%d]: got %q, want %q", i, tag, original.Tags[i])
		}
	}
}

func TestEncodeDecodeMap(t *testing.T) {
	original := map[string]any{
		"os":   "linux",
		"arch": "amd64",
		"cpus": 8,
	}

	data, err := bus.Encode(original)
	if err != nil {
		t.Fatalf("encode map failed: %v", err)
	}

	var decoded map[string]any
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatalf("decode map failed: %v", err)
	}

	if decoded["os"] != "linux" {
		t.Errorf("os: got %v, want linux", decoded["os"])
	}
	if decoded["arch"] != "amd64" {
		t.Errorf("arch: got %v, want amd64", decoded["arch"])
	}
}

func TestDecodeInvalidData(t *testing.T) {
	var v string
	err := bus.Decode([]byte{0xff, 0xfe, 0xfd}, &v)
	if err == nil {
		t.Error("expected error decoding invalid data")
	}
}

func TestMustEncodeValid(t *testing.T) {
	data := bus.MustEncode("hello")
	if len(data) == 0 {
		t.Error("MustEncode returned empty data")
	}
}

// --- KV Tests ---

func TestCreateAndUseKVBucket(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{
		Bucket:      "test-bucket",
		Description: "test",
		History:     3,
	})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	type fact struct {
		OS   string `msgpack:"os"`
		Arch string `msgpack:"arch"`
	}

	original := fact{OS: "linux", Arch: "amd64"}
	rev, err := bus.KVPut(ctx, kv, "web-01", original)
	if err != nil {
		t.Fatalf("kv put: %v", err)
	}
	if rev == 0 {
		t.Error("revision should not be 0")
	}

	var retrieved fact
	if err := bus.KVGet(ctx, kv, "web-01", &retrieved); err != nil {
		t.Fatalf("kv get: %v", err)
	}

	if retrieved.OS != "linux" || retrieved.Arch != "amd64" {
		t.Errorf("got %+v, want %+v", retrieved, original)
	}
}

func TestGetBucket(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.CreateBucket(ctx, js, bus.BucketConfig{
		Bucket: "my-bucket",
	})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	kv, err := bus.GetBucket(ctx, js, "my-bucket")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if kv == nil {
		t.Fatal("bucket is nil")
	}
}

func TestGetBucketNotFound(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.GetBucket(ctx, js, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent bucket")
	}
}

func TestDeleteBucket(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.CreateBucket(ctx, js, bus.BucketConfig{
		Bucket: "temp-bucket",
	})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	if err := bus.DeleteBucket(ctx, js, "temp-bucket"); err != nil {
		t.Fatalf("delete bucket: %v", err)
	}

	_, err = bus.GetBucket(ctx, js, "temp-bucket")
	if err == nil {
		t.Fatal("expected error after deleting bucket")
	}
}

func TestCreateDefaultBuckets(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	buckets, err := bus.CreateDefaultBuckets(ctx, js)
	if err != nil {
		t.Fatalf("create default buckets: %v", err)
	}

	expected := []string{
		bus.BucketFacts, bus.BucketBasket, bus.BucketJobs, bus.BucketJobReturns,
		bus.BucketPeelHeartbeat, bus.BucketLeases,
	}
	for _, name := range expected {
		if _, ok := buckets[name]; !ok {
			t.Errorf("missing bucket %q", name)
		}
	}
}

func TestCreateDefaultStreams(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	streams, err := bus.CreateDefaultStreams(ctx, js)
	if err != nil {
		t.Fatalf("create default streams: %v", err)
	}

	if _, ok := streams[bus.StreamJobEvents]; !ok {
		t.Error("missing job-events stream")
	}
}

func TestInitializeStorage(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	// Verify buckets exist.
	for _, name := range []string{bus.BucketFacts, bus.BucketBasket, bus.BucketJobs, bus.BucketJobReturns} {
		_, err := bus.GetBucket(ctx, js, name)
		if err != nil {
			t.Errorf("bucket %q not found after init: %v", name, err)
		}
	}
}

// --- BumpRevision Tests ---

func TestBumpRevision_SequentialIncrements(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: "test-rev"})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, "test-rev")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	for i := 1; i <= 3; i++ {
		if err := bus.BumpRevision(ctx, kv); err != nil {
			t.Fatalf("bump %d: %v", i, err)
		}
		entry, err := kv.Get(ctx, bus.KeyRevision)
		if err != nil {
			t.Fatalf("get revision after bump %d: %v", i, err)
		}
		want := fmt.Sprintf("%d", i)
		if got := string(entry.Value()); got != want {
			t.Errorf("bump %d: got %q, want %q", i, got, want)
		}
	}
}

func TestBumpRevision_StartsFromZero(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: "test-rev2"})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, "test-rev2")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// First bump on empty bucket should produce "1".
	if err := bus.BumpRevision(ctx, kv); err != nil {
		t.Fatalf("bump: %v", err)
	}
	entry, err := kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := string(entry.Value()); got != "1" {
		t.Errorf("got %q, want %q", got, "1")
	}
}

func TestBumpRevision_ConcurrentBumps(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	_, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: "test-rev3"})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	kv, err := bus.GetBucket(ctx, js, "test-rev3")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}

	// The bucket starts empty, so the first successful bump exercises the
	// Create path and racing goroutines exercise the Create/Update CAS
	// retry. No increment may be lost: final value == total bumps.
	const (
		goroutines    = 4
		bumpsPerGoro  = 10
		expectedTotal = goroutines * bumpsPerGoro
	)

	errCh := make(chan error, expectedTotal)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < bumpsPerGoro; i++ {
				if err := bus.BumpRevision(ctx, kv); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent bump failed: %v", err)
	}

	entry, err := kv.Get(ctx, bus.KeyRevision)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	want := fmt.Sprintf("%d", expectedTotal)
	if got := string(entry.Value()); got != want {
		t.Errorf("lost updates: final revision %q, want %q", got, want)
	}
}

// --- Client Tests ---

func TestClientRequiresURLs(t *testing.T) {
	_, err := bus.NewClient(bus.ClientConfig{})
	if err == nil {
		t.Fatal("expected error when no URLs provided")
	}
}

func TestNewClientRetryOnFailedConnect(t *testing.T) {
	// Port 1 on localhost refuses connections immediately. With
	// RetryConnect set, NewClient must return a client in reconnecting
	// state instead of a hard error, so daemons can boot while NATS is
	// down. The event hooks are set to verify the ClientConfig wiring
	// compiles and is accepted; without a real server their invocation
	// cannot be exercised here.
	c, err := bus.NewClient(bus.ClientConfig{
		URLs:           []string{"nats://127.0.0.1:1"},
		RetryConnect:   true,
		MaxReconnects:  1,
		ReconnectWait:  10 * time.Millisecond,
		OnReconnect:    func() {},
		OnDisconnect:   func(error) {},
		OnSlowConsumer: func() {},
	})
	if err != nil {
		t.Fatalf("NewClient with unreachable NATS should not hard-fail: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()

	if c.IsHealthy() {
		t.Error("client should not report healthy while initial connect is retrying")
	}
	if c.Conn() == nil {
		t.Error("client should expose the underlying (reconnecting) connection")
	}
}

// fakeNATSServer is a minimal TCP server speaking just enough of the NATS
// client protocol (INFO banner, PONG replies) for a nats.go client to
// consider itself connected. It is NOT an embedded NATS server (project
// convention: no embedded NATS dependency) — it exists solely to exercise
// the client-side connect/handler wiring that a refused port cannot reach.
func fakeNATSServer(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go func(c net.Conn) {
				defer c.Close()
				host, port, _ := net.SplitHostPort(ln.Addr().String())
				fmt.Fprintf(c, "INFO {\"server_id\":\"fake\",\"server_name\":\"fake\",\"version\":\"2.10.14\",\"proto\":1,\"host\":%q,\"port\":%s,\"max_payload\":1048576}\r\n", host, port)
				buf := make([]byte, 4096)
				var pending []byte
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					pending = append(pending, buf[:n]...)
					for {
						idx := -1
						for i := 0; i+1 < len(pending); i++ {
							if pending[i] == '\r' && pending[i+1] == '\n' {
								idx = i
								break
							}
						}
						if idx < 0 {
							break
						}
						line := string(pending[:idx])
						pending = pending[idx+2:]
						if line == "PING" {
							fmt.Fprint(c, "PONG\r\n")
						}
					}
				}
			}(conn)
		}
	}()
}

// TestClientDeferredInitialConnectFlipsHealthy reproduces the offline-boot
// scenario (review finding C3): with RetryConnect, the client boots while
// the server port refuses connections, and the FIRST successful connection
// is a deferred one — nats.go fires ConnectedCB (not ReconnectedCB) for it,
// so before the fix neither handler ran, healthy stayed false forever, and
// the peel's connected phase never started. The test asserts that once the
// (fake) server comes up, IsHealthy flips true, OnReconnect fires, and
// ReconnectNotify signals. Real-server behavioral details (JetStream,
// reconnect-after-drop) still need the Docker integration stack; this
// covers the client-side handler wiring, which is where the bug lived.
func TestClientDeferredInitialConnectFlipsHealthy(t *testing.T) {
	// Reserve a port, then close the listener so the initial connect is
	// refused (the deferred-connect precondition).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	reconnected := make(chan struct{}, 8)
	c, err := bus.NewClient(bus.ClientConfig{
		URLs:          []string{"nats://" + addr},
		RetryConnect:  true,
		MaxReconnects: -1,
		ReconnectWait: 50 * time.Millisecond,
		OnReconnect:   func() { reconnected <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("NewClient with unreachable NATS should not hard-fail: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()

	if c.IsHealthy() {
		t.Fatal("client must not report healthy before the server exists")
	}

	// Bring the "server" up on the reserved port. The port could in
	// principle be grabbed by another process in the gap; retry briefly.
	var srv net.Listener
	for i := 0; i < 20; i++ {
		srv, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("re-listen on reserved port %s: %v", addr, err)
	}
	defer srv.Close()
	fakeNATSServer(t, srv)

	// The deferred initial connect must flip the client healthy.
	deadline := time.Now().Add(5 * time.Second)
	for !c.IsHealthy() {
		if time.Now().After(deadline) {
			t.Fatal("IsHealthy never became true after the deferred initial connect (ConnectedCB not handled?)")
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Error("OnReconnect hook did not fire on the deferred initial connect")
	}
	select {
	case <-c.ReconnectNotify():
	case <-time.After(2 * time.Second):
		t.Error("ReconnectNotify did not signal on the deferred initial connect")
	}
}

func TestNewClientFailsFastWithoutRetryConnect(t *testing.T) {
	// Without RetryConnect (the operator-CLI configuration), an
	// unreachable server must be an immediate, clear error — not a client
	// that times out later on every request.
	c, err := bus.NewClient(bus.ClientConfig{
		URLs:          []string{"nats://127.0.0.1:1"},
		MaxReconnects: 1,
		ReconnectWait: 10 * time.Millisecond,
	})
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
		t.Fatal("NewClient without RetryConnect should hard-fail on unreachable NATS")
	}
}

// --- TLS Tests ---

func TestGenerateSelfSignedCA(t *testing.T) {
	ca, err := bus.GenerateSelfSignedCA("Zester Test", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	if len(ca.CertPEM) == 0 {
		t.Error("CA cert PEM is empty")
	}
	if len(ca.KeyPEM) == 0 {
		t.Error("CA key PEM is empty")
	}
	if ca.Cert == nil {
		t.Error("CA cert is nil")
	}
	if !ca.Cert.IsCA {
		t.Error("certificate should be CA")
	}
	if ca.Cert.Subject.CommonName != "Zester Test CA" {
		t.Errorf("CA CN: got %q, want %q", ca.Cert.Subject.CommonName, "Zester Test CA")
	}
}

func TestIssueCert(t *testing.T) {
	ca, err := bus.GenerateSelfSignedCA("Zester Test", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	certPEM, keyPEM, err := ca.IssueCert(
		"test-server",
		[]net.IP{net.ParseIP("127.0.0.1")},
		[]string{"localhost"},
		24*time.Hour,
	)
	if err != nil {
		t.Fatalf("issue cert: %v", err)
	}

	if len(certPEM) == 0 {
		t.Error("cert PEM is empty")
	}
	if len(keyPEM) == 0 {
		t.Error("key PEM is empty")
	}

	// Verify the cert can be loaded as a tls.Certificate.
	_, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load cert pair: %v", err)
	}
}

func TestServerTLSConfig(t *testing.T) {
	ca, err := bus.GenerateSelfSignedCA("Zester Test", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	certPEM, keyPEM, err := ca.IssueCert(
		"test-server",
		[]net.IP{net.ParseIP("127.0.0.1")},
		[]string{"localhost"},
		24*time.Hour,
	)
	if err != nil {
		t.Fatalf("issue cert: %v", err)
	}

	bus.WritePEM(caFile, ca.CertPEM)
	bus.WritePEM(certFile, certPEM)
	bus.WritePEM(keyFile, keyPEM)

	tlsCfg, err := bus.ServerTLSConfig(bus.TLSConfig{
		CertFile:     certFile,
		KeyFile:      keyFile,
		CAFile:       caFile,
		VerifyClient: true,
	})
	if err != nil {
		t.Fatalf("server TLS config: %v", err)
	}

	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Error("min TLS version should be 1.3")
	}
	if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Error("client auth should require verification")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Error("should have one certificate")
	}
}

func TestClientTLSConfig(t *testing.T) {
	ca, err := bus.GenerateSelfSignedCA("Zester Test", 24*time.Hour)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	certPEM, keyPEM, err := ca.IssueCert(
		"test-client",
		nil,
		nil,
		24*time.Hour,
	)
	if err != nil {
		t.Fatalf("issue cert: %v", err)
	}

	bus.WritePEM(caFile, ca.CertPEM)
	bus.WritePEM(certFile, certPEM)
	bus.WritePEM(keyFile, keyPEM)

	tlsCfg, err := bus.ClientTLSConfig(bus.TLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
		CAFile:   caFile,
	})
	if err != nil {
		t.Fatalf("client TLS config: %v", err)
	}

	if tlsCfg.MinVersion != tls.VersionTLS13 {
		t.Error("min TLS version should be 1.3")
	}
	if tlsCfg.RootCAs == nil {
		t.Error("root CAs should be set")
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Error("should have one client certificate")
	}
}

func TestWritePEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pem")

	data := []byte("test PEM data")
	if err := bus.WritePEM(path, data); err != nil {
		t.Fatalf("write PEM: %v", err)
	}

	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read PEM: %v", err)
	}
	if string(read) != string(data) {
		t.Errorf("content mismatch")
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestKVGetAll(t *testing.T) {
	ctx := context.Background()

	t.Run("empty bucket", func(t *testing.T) {
		kv := bustest.NewFakeKV("test-empty", 0)
		result, err := bus.KVGetAll[map[string]any](ctx, kv)
		if err != nil {
			t.Fatalf("KVGetAll on empty bucket: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty map, got %d entries", len(result))
		}
	})

	t.Run("populated bucket", func(t *testing.T) {
		kv := bustest.NewFakeKV("test-pop", 0)
		facts := map[string]map[string]any{
			"peel-1": {"os": "ubuntu", "env": "prod"},
			"peel-2": {"os": "centos", "env": "staging"},
			"peel-3": {"os": "debian", "env": "dev"},
		}
		for k, v := range facts {
			data, err := bus.Encode(v)
			if err != nil {
				t.Fatalf("encode %s: %v", k, err)
			}
			if _, err := kv.Put(ctx, k, data); err != nil {
				t.Fatalf("put %s: %v", k, err)
			}
		}

		result, err := bus.KVGetAll[map[string]any](ctx, kv)
		if err != nil {
			t.Fatalf("KVGetAll: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(result))
		}
		for k, want := range facts {
			got, ok := result[k]
			if !ok {
				t.Errorf("missing key %q", k)
				continue
			}
			if got["os"] != want["os"] || got["env"] != want["env"] {
				t.Errorf("key %q: got %v, want %v", k, got, want)
			}
		}
	})

	t.Run("deleted keys excluded", func(t *testing.T) {
		kv := bustest.NewFakeKV("test-del", 0)
		for _, k := range []string{"a", "b", "c"} {
			data, _ := bus.Encode(map[string]any{"id": k})
			kv.Put(ctx, k, data)
		}
		kv.Delete(ctx, "b")

		result, err := bus.KVGetAll[map[string]any](ctx, kv)
		if err != nil {
			t.Fatalf("KVGetAll: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 entries (b deleted), got %d: %v", len(result), result)
		}
		if _, ok := result["b"]; ok {
			t.Error("deleted key 'b' should not be present")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		// FakeKV sends the nil sentinel synchronously into a buffered channel,
		// so cancellation only matters when there are entries to process.
		// Use a real cancelled context with populated data to verify the
		// select branch works.
		cancelCtx, cancel := context.WithCancel(ctx)
		kv := bustest.NewFakeKV("test-cancel", 0)
		// Populate so WatchAll has entries to send before nil sentinel.
		for i := range 100 {
			data, _ := bus.Encode(map[string]any{"i": i})
			kv.Put(ctx, fmt.Sprintf("k%d", i), data)
		}
		cancel()
		// With a cancelled context and a full channel, the select should
		// eventually pick up ctx.Done. If it reads all entries + nil first,
		// that's also acceptable since FakeKV is synchronous.
		result, err := bus.KVGetAll[map[string]any](cancelCtx, kv)
		// Either context error or successful drain are acceptable.
		if err != nil && err != context.Canceled {
			t.Fatalf("unexpected error: %v", err)
		}
		if err == nil && len(result) != 100 {
			t.Errorf("expected 100 entries or context error, got %d entries", len(result))
		}
	})
}

// --- Reactor Event Subject Tests ---

func TestEventOriginAndReactorConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"origin_master", bus.OriginMaster, "_master"},
		{"origin_admin", bus.OriginAdmin, "_admin"},
		{"event_send_token", bus.SubjectEventSend, "send"},
		{"reactor_test", bus.SubjectReactorTest, "zester.reactor.test"},
		{"stream_events", bus.StreamEvents, "events"},
		{"bucket_reactor_files", bus.BucketReactorFiles, "reactor-files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

func TestEventSubjectHelpers(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"peel_send", bus.PeelEventSendSubject("web-01", "myco.deploy.finished"), "zester.event.web-01.send.myco.deploy.finished"},
		{"peel_send_single", bus.PeelEventSendSubject("web-01", "ping"), "zester.event.web-01.send.ping"},
		{"master", bus.MasterEventSubject("enroll.pending.enr-1"), "zester.event._master.enroll.pending.enr-1"},
		{"master_reaction", bus.MasterEventSubject("reaction.chain.next"), "zester.event._master.reaction.chain.next"},
		{"admin_send", bus.AdminEventSendSubject("myco.deploy.finished"), "zester.event._admin.send.myco.deploy.finished"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

// TestEventSubjectHelpersCoveredByStreamWildcard proves every event helper
// publishes under the events stream's capture subject, so nothing an event
// producer emits can bypass the durable log.
func TestEventSubjectHelpersCoveredByStreamWildcard(t *testing.T) {
	prefix := bus.SubjectEvent + "." // "zester.event." per EventSubjectAll()
	for _, subject := range []string{
		bus.PeelEventSendSubject("web-01", "myco.deploy"),
		bus.MasterEventSubject("enroll.pending.enr-1"),
		bus.AdminEventSendSubject("chain.next"),
		bus.BeaconSubject("web-01", "service"),
	} {
		if len(subject) <= len(prefix) || subject[:len(prefix)] != prefix {
			t.Errorf("subject %q not under %q", subject, prefix)
		}
	}
}

// --- Events Stream & Reactor Bucket Tests ---

func TestDefaultEventsStream(t *testing.T) {
	cfg := bus.DefaultEventsStream()

	if cfg.Name != bus.StreamEvents {
		t.Errorf("name: got %q, want %q", cfg.Name, bus.StreamEvents)
	}
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != "zester.event.>" {
		t.Errorf("subjects: got %v, want [zester.event.>]", cfg.Subjects)
	}
	if cfg.MaxAge != 7*24*time.Hour {
		t.Errorf("max age: got %v, want %v", cfg.MaxAge, 7*24*time.Hour)
	}
	if cfg.Duplicates != 2*time.Minute {
		t.Errorf("duplicates window: got %v, want %v", cfg.Duplicates, 2*time.Minute)
	}
	if cfg.MaxBytes == 0 || cfg.MaxMsgs == 0 {
		t.Errorf("flood bounds must be set: MaxBytes=%d MaxMsgs=%d", cfg.MaxBytes, cfg.MaxMsgs)
	}
	if cfg.Retention != jetstream.LimitsPolicy {
		t.Errorf("retention: got %v, want LimitsPolicy", cfg.Retention)
	}
	if cfg.Storage != jetstream.FileStorage {
		t.Errorf("storage: got %v, want FileStorage", cfg.Storage)
	}
	if cfg.Tier != bus.TierCritical {
		t.Errorf("tier: got %v, want TierCritical", cfg.Tier)
	}
}

func TestCreateDefaultStreamsIncludesEvents(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	streams, err := bus.CreateDefaultStreams(ctx, js)
	if err != nil {
		t.Fatalf("create default streams: %v", err)
	}
	for _, name := range []string{bus.StreamJobEvents, bus.StreamEvents} {
		if _, ok := streams[name]; !ok {
			t.Errorf("missing stream %q", name)
		}
	}

	// The Duplicates window must survive the bus.StreamConfig ->
	// jetstream.StreamConfig mapping (MsgID dedup depends on it).
	got := js.streamConfig(bus.StreamEvents)
	if got.Duplicates != 2*time.Minute {
		t.Errorf("events stream duplicate window: got %v, want %v", got.Duplicates, 2*time.Minute)
	}
	if len(got.Subjects) != 1 || got.Subjects[0] != "zester.event.>" {
		t.Errorf("events stream subjects: got %v, want [zester.event.>]", got.Subjects)
	}
	// job-events must be untouched by the events stream addition: its own
	// subjects, no duplicate window.
	je := js.streamConfig(bus.StreamJobEvents)
	if len(je.Subjects) != 1 || je.Subjects[0] != "zester.job.>" {
		t.Errorf("job-events subjects changed: got %v", je.Subjects)
	}
	if je.Duplicates != 0 {
		t.Errorf("job-events gained a duplicate window: %v", je.Duplicates)
	}
}

func TestInitializeStorageOptsTiersEventsStream(t *testing.T) {
	js := newTestJS()
	ctx := context.Background()

	if err := bus.InitializeStorageOpts(ctx, js, bus.StorageOptions{ClusterSize: 3}); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	// The events stream gets the same replicas tiering as job-events.
	for _, name := range []string{bus.StreamJobEvents, bus.StreamEvents} {
		if got := js.streamConfig(name).Replicas; got != 3 {
			t.Errorf("stream %q: replicas = %d, want 3", name, got)
		}
	}
	// And the reactor-files bucket exists with the effective count.
	if got := js.kvConfig(bus.BucketReactorFiles).Replicas; got != 3 {
		t.Errorf("bucket %q: replicas = %d, want 3", bus.BucketReactorFiles, got)
	}
}

func TestDefaultBucketsIncludesReactorFiles(t *testing.T) {
	for _, cfg := range bus.DefaultBuckets() {
		if cfg.Bucket != bus.BucketReactorFiles {
			continue
		}
		if cfg.History != 3 {
			t.Errorf("reactor-files history: got %d, want 3", cfg.History)
		}
		if cfg.Tier != bus.TierCritical {
			t.Errorf("reactor-files tier: got %v, want TierCritical", cfg.Tier)
		}
		if cfg.TTL != 0 {
			t.Errorf("reactor-files must not expire, got TTL %v", cfg.TTL)
		}
		return
	}
	t.Fatalf("bucket %q missing from DefaultBuckets", bus.BucketReactorFiles)
}
