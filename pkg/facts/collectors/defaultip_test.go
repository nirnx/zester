package collectors_test

import (
	"context"
	"net"
	"testing"

	"github.com/ptorbus/zester/pkg/facts/collectors"
)

func TestDefaultIP_Metadata(t *testing.T) {
	c := collectors.DefaultIP{}
	if c.Name() != "default_ipv4" {
		t.Errorf("Name() = %q, want %q", c.Name(), "default_ipv4")
	}
	if c.Interval() != 0 {
		t.Errorf("Interval() = %v, want 0", c.Interval())
	}
	if !c.MergeAtRoot() {
		t.Error("MergeAtRoot() = false, want true")
	}
}

func TestDefaultIP_Collect(t *testing.T) {
	c := collectors.DefaultIP{}
	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	ipStr, ok := result["default_ipv4"].(string)
	if !ok {
		t.Fatal("default_ipv4 not found or not a string")
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		t.Errorf("default_ipv4 = %q, not a valid IP", ipStr)
	}
	if ip.To4() == nil {
		t.Errorf("default_ipv4 = %q, not an IPv4 address", ipStr)
	}
}
