//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const integrationAPITokenPath = "/data/auth/api-tokens/integration.token"

type apiDispatchResp struct {
	JID     string   `json:"jid"`
	Targets []string `json:"targets"`
	Status  string   `json:"status"`
}

type apiJobResp struct {
	JID      string `json:"jid"`
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
}

func apiCall(t *testing.T, method, path, body string, withAuth bool) (int, string) {
	t.Helper()
	authHeader := ""
	if withAuth {
		authHeader = `-H "Authorization: Bearer $(cat ` + integrationAPITokenPath + `)"`
	}
	bodyArg := ""
	if body != "" {
		bodyArg = `-H "Content-Type: application/json" --data '` + body + `'`
	}
	cmd := []string{
		"sh",
		"-lc",
		fmt.Sprintf(`status=$(curl -sk -w "%%{http_code}" -o /tmp/api.out -X %s %s %s https://master:8443%s); cat /tmp/api.out; echo; echo "__STATUS__:$status"`,
			method, authHeader, bodyArg, path),
	}

	out := execInContainer(t, "admin", cmd)
	idx := strings.LastIndex(out, "__STATUS__:")
	if idx == -1 {
		t.Fatalf("missing status marker in output: %s", out)
	}
	bodyOut := strings.TrimSpace(out[:idx])
	var status int
	if _, err := fmt.Sscanf(strings.TrimSpace(out[idx:]), "__STATUS__:%d", &status); err != nil {
		t.Fatalf("parse status: %v output=%s", err, out)
	}
	return status, bodyOut
}

func TestAPIUnauthorized(t *testing.T) {
	status, _ := apiCall(t, "GET", "/api/v1/enrollments", "", false)
	if status != 401 {
		t.Fatalf("expected 401, got %d", status)
	}
}

func TestAPIDocsEndpoints(t *testing.T) {
	status, body := apiCall(t, "GET", "/api/v1/docs", "", false)
	if status != 200 {
		t.Fatalf("expected 200 for docs, got %d", status)
	}
	if !strings.Contains(strings.ToLower(body), "swagger") {
		t.Fatalf("docs response missing swagger marker: %s", body)
	}

	status, body = apiCall(t, "GET", "/api/v1/openapi.yaml", "", false)
	if status != 200 {
		t.Fatalf("expected 200 for openapi.yaml, got %d", status)
	}
	if !strings.Contains(body, "/jobs") {
		t.Fatalf("openapi.yaml missing /jobs path")
	}
}

func TestAPIDispatchAndPoll(t *testing.T) {
	status, body := apiCall(t, "POST", "/api/v1/jobs", `{"target":"*","function":"test.ping","timeout":"5s"}`, true)
	if status != 202 {
		t.Fatalf("expected 202 dispatch, got %d body=%s", status, body)
	}

	var disp apiDispatchResp
	if err := json.Unmarshal([]byte(body), &disp); err != nil {
		t.Fatalf("decode dispatch response: %v body=%s", err, body)
	}
	if disp.JID == "" {
		t.Fatalf("dispatch returned empty jid: %+v", disp)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, body = apiCall(t, "GET", "/api/v1/jobs/"+disp.JID, "", true)
		if status != 200 {
			t.Fatalf("expected 200 get job, got %d body=%s", status, body)
		}
		var jr apiJobResp
		if err := json.Unmarshal([]byte(body), &jr); err != nil {
			t.Fatalf("decode job response: %v body=%s", err, body)
		}
		if jr.Complete {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("job %s did not complete in time", disp.JID)
}

func TestAPIListEnrollments(t *testing.T) {
	status, body := apiCall(t, "GET", "/api/v1/enrollments?state=all", "", true)
	if status != 200 {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		t.Fatalf("decode enrollments: %v body=%s", err, body)
	}
	if len(arr) == 0 {
		t.Fatal("expected at least one enrollment record")
	}
}
