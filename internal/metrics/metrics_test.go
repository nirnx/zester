package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape renders the registry's /metrics output as a string.
func scrape(t *testing.T, m *Registry) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("scrape: status %d", resp.StatusCode)
	}
	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestNewMasterRegistryDefinesMetrics(t *testing.T) {
	m := NewMasterRegistry()

	// Counters must exist and be incrementable without panicking.
	m.JobsTotal.WithLabelValues("complete").Inc()
	m.JobDuration.WithLabelValues("test.ping").Observe(0.5)
	m.JobReclaims.Inc()
	m.FactsSyncTotal.Inc()
	m.NATSReconnects.Inc()
	m.NATSDisconnects.Inc()
	m.NATSSlowConsumers.Inc()

	out := scrape(t, m)
	for _, want := range []string{
		`zester_jobs_total{status="complete"} 1`,
		`zester_job_reclaims_total 1`,
		`zester_facts_sync_total 1`,
		`zester_nats_reconnects_total 1`,
		`zester_nats_disconnects_total 1`,
		`zester_nats_slow_consumers_total 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape output missing %q", want)
		}
	}
	if !strings.Contains(out, "zester_job_duration_seconds") {
		t.Error("scrape output missing zester_job_duration_seconds histogram")
	}
}

func TestNewPeelRegistryConstructs(t *testing.T) {
	m := NewPeelRegistry()

	m.PeelConnected.Set(1)
	m.NATSReconnects.Inc()
	m.NATSSlowConsumers.Inc()

	out := scrape(t, m)
	if !strings.Contains(out, "zester_peel_connected 1") {
		t.Error("scrape output missing zester_peel_connected")
	}
	if !strings.Contains(out, "zester_nats_reconnects_total 1") {
		t.Error("scrape output missing zester_nats_reconnects_total")
	}
	if !strings.Contains(out, "zester_nats_slow_consumers_total 1") {
		t.Error("scrape output missing zester_nats_slow_consumers_total")
	}
}
