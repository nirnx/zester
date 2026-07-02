package exectest

import (
	"context"
	"sync"
)

// fakeService holds the state of a single service entry.
type fakeService struct {
	running bool
	enabled bool
}

// FakeServiceExec is an in-memory fake for exec.ServiceExec.
// It stores service states in a map and supports error injection for testing.
type FakeServiceExec struct {
	mu       sync.Mutex
	name     string
	services map[string]*fakeService

	// Error injection fields — when set, the corresponding method returns the error.
	StartErr   error
	StopErr    error
	RestartErr error
	EnableErr  error
	DisableErr error
}

// NewFakeServiceExec creates a FakeServiceExec with the given provider name.
func NewFakeServiceExec(name string) *FakeServiceExec {
	return &FakeServiceExec{
		name:     name,
		services: make(map[string]*fakeService),
	}
}

func (f *FakeServiceExec) Name() string { return f.name }

func (f *FakeServiceExec) IsRunning(_ context.Context, service string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[service]
	if !ok {
		return false, nil
	}
	return svc.running, nil
}

func (f *FakeServiceExec) IsEnabled(_ context.Context, service string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[service]
	if !ok {
		return false, nil
	}
	return svc.enabled, nil
}

func (f *FakeServiceExec) Start(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		return f.StartErr
	}
	if _, ok := f.services[service]; !ok {
		f.services[service] = &fakeService{}
	}
	f.services[service].running = true
	return nil
}

func (f *FakeServiceExec) Stop(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StopErr != nil {
		return f.StopErr
	}
	if svc, ok := f.services[service]; ok {
		svc.running = false
	}
	return nil
}

func (f *FakeServiceExec) Restart(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RestartErr != nil {
		return f.RestartErr
	}
	if _, ok := f.services[service]; !ok {
		f.services[service] = &fakeService{}
	}
	f.services[service].running = true
	return nil
}

func (f *FakeServiceExec) Enable(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.EnableErr != nil {
		return f.EnableErr
	}
	if _, ok := f.services[service]; !ok {
		f.services[service] = &fakeService{}
	}
	f.services[service].enabled = true
	return nil
}

func (f *FakeServiceExec) Disable(_ context.Context, service string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DisableErr != nil {
		return f.DisableErr
	}
	if svc, ok := f.services[service]; ok {
		svc.enabled = false
	}
	return nil
}

// PreAdd places a service in the fake store (for test setup).
func (f *FakeServiceExec) PreAdd(service string, running, enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services[service] = &fakeService{running: running, enabled: enabled}
}

// IsRunningSync returns the running state synchronously (for test assertions).
func (f *FakeServiceExec) IsRunningSync(service string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[service]
	if !ok {
		return false
	}
	return svc.running
}

// IsEnabledSync returns the enabled state synchronously (for test assertions).
func (f *FakeServiceExec) IsEnabledSync(service string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[service]
	if !ok {
		return false
	}
	return svc.enabled
}
