package enroll_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
)

func TestAdminRequestRoundTrip(t *testing.T) {
	req := enroll.AdminRequest{
		ID:       "enr-123",
		Operator: "alice",
		Reason:   "compromised host",
	}
	data, err := bus.Encode(&req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var got enroll.AdminRequest
	if err := bus.Decode(data, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got != req {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, req)
	}
}

func TestAdminResponseRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resp := enroll.AdminResponse{
		Record: &enroll.Record{
			ID:        "enr-123",
			PeelID:    "web-01",
			State:     enroll.StateApproved,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	data, err := bus.Encode(&resp)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var got enroll.AdminResponse
	if err := bus.Decode(data, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Err != "" {
		t.Fatalf("unexpected error field: %q", got.Err)
	}
	if got.Record == nil || got.Record.ID != "enr-123" || got.Record.State != enroll.StateApproved {
		t.Fatalf("record mismatch: %+v", got.Record)
	}
	if err := got.AsError(); err != nil {
		t.Fatalf("AsError on success: %v", err)
	}
}

func TestAdminResponseAsError(t *testing.T) {
	resp := enroll.AdminResponse{Err: "boom"}
	err := resp.AsError()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected error 'boom', got %v", err)
	}
}

func TestAdminRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     enroll.AdminRequest
		wantErr string
	}{
		{"valid", enroll.AdminRequest{ID: "enr-1", Operator: "op"}, ""},
		{"valid with reason", enroll.AdminRequest{ID: "enr-1", Operator: "op", Reason: "why"}, ""},
		{"missing id", enroll.AdminRequest{Operator: "op"}, "id is required"},
		{"missing operator", enroll.AdminRequest{ID: "enr-1"}, "operator is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
