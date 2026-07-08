package enroll

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapDoc_ContentEqualIgnoresIssuedAt(t *testing.T) {
	a := BootstrapDoc{V: 1, CABundlePEM: "PEM", Fingerprint: "sha256:x", NATSURLs: []string{"tls://a:4222"}, IssuedAt: "t1"}
	b := a
	b.IssuedAt = "t2"
	if !a.ContentEqual(b) {
		t.Error("docs differing only in IssuedAt should be content-equal")
	}
	b.NATSURLs = []string{"tls://a:4222", "tls://b:4222"}
	if a.ContentEqual(b) {
		t.Error("docs with different NATS URLs must not be content-equal")
	}
}

func TestHandleBootstrap_ServesDocOrIsAbsent(t *testing.T) {
	// With a provider: route is registered and serves the doc, filling V.
	h := NewHandler(HandlerConfig{
		Bootstrap: func() BootstrapDoc {
			return BootstrapDoc{CABundlePEM: "ROOTPEM", Fingerprint: "sha256:abc", NATSURLs: []string{"tls://nats:4222"}}
		},
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/enroll/ca", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var doc BootstrapDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.V != BootstrapVersion || doc.CABundlePEM != "ROOTPEM" || len(doc.NATSURLs) != 1 {
		t.Errorf("unexpected doc: %+v", doc)
	}

	// Without a provider: the route is not registered (404).
	h2 := NewHandler(HandlerConfig{})
	mux2 := http.NewServeMux()
	h2.RegisterRoutes(mux2)
	rr2 := httptest.NewRecorder()
	mux2.ServeHTTP(rr2, httptest.NewRequest("GET", "/api/v1/enroll/ca", nil))
	if rr2.Code != http.StatusNotFound {
		t.Errorf("no-provider status = %d, want 404", rr2.Code)
	}
}
