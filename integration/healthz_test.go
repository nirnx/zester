//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLocalHealthzEndpoints(t *testing.T) {
	services := map[string]string{
		"master":   "master",
		"master-2": "master",
		"web-01":   "peel",
		"web-02":   "peel",
		"web-03":   "peel",
		"db-01":    "peel",
		"db-02":    "peel",
	}

	// The daemons bind /healthz on distinct default ports so a colocated
	// master + peel do not collide: peel 127.0.0.1:9090, master :9091.
	healthPort := map[string]string{
		"peel":   "9090",
		"master": "9091",
	}

	for service, wantComponent := range services {
		service := service
		wantComponent := wantComponent
		t.Run(service, func(t *testing.T) {
			port := healthPort[wantComponent]
			// Probe from inside the container because healthz is bound to loopback.
			out := execInContainer(t, service, []string{
				"sh", "-lc",
				fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS http://127.0.0.1:%s/healthz; else wget -qO- http://127.0.0.1:%s/healthz; fi`, port, port),
			})

			var body map[string]any
			if err := json.Unmarshal([]byte(out), &body); err != nil {
				t.Fatalf("decode healthz JSON for %s: %v (raw=%q)", service, err, out)
			}

			if got, _ := body["status"].(string); got != "ok" {
				t.Fatalf("healthz status for %s = %q, want %q (body=%v)", service, got, "ok", body)
			}
			if got, _ := body["component"].(string); got != wantComponent {
				t.Fatalf("healthz component for %s = %q, want %q (body=%v)", service, got, wantComponent, body)
			}

			t.Logf("healthz %s ok: %s", service, fmt.Sprintf("%v", body))
		})
	}
}
