package collectors

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/host"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// Network collects network-related facts.
type Network struct{}

func (Network) Name() string            { return "network" }
func (Network) Interval() time.Duration { return 0 }

func (Network) Collect(ctx context.Context) (map[string]any, error) {
	result := make(map[string]any)

	// Hostname.
	hostname, err := os.Hostname()
	if err == nil {
		result["hostname"] = hostname
	}

	// FQDN from gopsutil host info or DNS lookup.
	if info, err := host.InfoWithContext(ctx); err == nil && info.Hostname != "" {
		result["fqdn"] = info.Hostname
	} else if hostname != "" {
		result["fqdn"] = hostname
	}

	// IP addresses and interfaces.
	var ipv4s []string
	var ipv6s []string
	var ifaceList []map[string]any

	ifaces, err := psnet.InterfacesWithContext(ctx)
	if err == nil {
		for _, iface := range ifaces {
			ifInfo := map[string]any{
				"name":  iface.Name,
				"mac":   iface.HardwareAddr,
				"flags": iface.Flags,
			}

			var addrs []string
			for _, addr := range iface.Addrs {
				addrs = append(addrs, addr.Addr)
				ip, _, _ := net.ParseCIDR(addr.Addr)
				if ip == nil {
					continue
				}
				if ip.To4() != nil {
					ipv4s = append(ipv4s, ip.String())
				} else {
					ipv6s = append(ipv6s, ip.String())
				}
			}
			ifInfo["addrs"] = addrs
			ifaceList = append(ifaceList, ifInfo)
		}
	}

	result["ipv4"] = ipv4s
	result["ipv6"] = ipv6s
	result["interfaces"] = ifaceList

	return result, nil
}
