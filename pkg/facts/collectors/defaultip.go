package collectors

import (
	"context"
	"net"
	"time"
)

// DefaultIP determines the peel's default outgoing IPv4 address.
// It uses the UDP dial trick: connect to a non-routable address (TEST-NET-1,
// RFC 5737) without sending data, then read the local address from the socket.
// The result is merged at the root level so it appears as facts["default_ipv4"].
type DefaultIP struct{}

func (DefaultIP) Name() string            { return "default_ipv4" }
func (DefaultIP) Interval() time.Duration { return 0 }

// MergeAtRoot implements facts.RootMerger so the result appears as a
// top-level fact rather than nested under "default_ipv4".
func (DefaultIP) MergeAtRoot() bool { return true }

func (DefaultIP) Collect(_ context.Context) (map[string]any, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return map[string]any{}, nil
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return map[string]any{
		"default_ipv4": addr.IP.String(),
	}, nil
}
