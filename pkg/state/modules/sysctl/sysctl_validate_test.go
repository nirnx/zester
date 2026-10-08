package sysctlmod

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// TestSysctlPresentRejectsInvalidKey pins the builder-tail key allowlist: the
// key is passed to `sysctl -w` and written as the left-hand side of a
// `key = value` drop-in line, so whitespace, `=`, or a newline would corrupt
// the drop-in or target a different parameter.
func TestSysctlPresentRejectsInvalidKey(t *testing.T) {
	build := NewSysctlPresentBuilder(testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec()), modschema.DecodeOptions{})
	bad := []string{
		"net.ipv4.ip_forward\nkernel.sysrq = 1",
		"net.ipv4.ip_forward = 1",
		"net ipv4",
		"kernel.sysrq=1",
		"a;b",
		"$(id)",
	}
	for _, key := range bad {
		_, err := build("x", map[string]any{"name": key, "value": "1"})
		if err == nil {
			t.Errorf("key %q: expected builder error, got nil", key)
			continue
		}
		if !strings.Contains(err.Error(), "kernel parameter name") {
			t.Errorf("key %q: error %q should say the parameter name is invalid", key, err)
		}
	}
	for _, key := range []string{"net.ipv4.ip_forward", "vm.swappiness", "net/ipv4/ip_forward", "net.ipv4.conf.eth0/100.rp_filter", "net.ipv4.conf.br-lan.rp_filter", "kernel.sched_rt_runtime_us"} {
		if _, err := build(key, map[string]any{"value": "1"}); err != nil {
			t.Errorf("key %q: unexpected builder error %v", key, err)
		}
	}
}

// TestSysctlPresentRejectsControlCharsInValue pins the value validation: the
// value is written verbatim after `key = `, so a newline appends an extra,
// unmanaged kernel setting to the drop-in.
func TestSysctlPresentRejectsControlCharsInValue(t *testing.T) {
	build := NewSysctlPresentBuilder(testSysctlMctx(exectest.NewFakeSysctlExec(), exectest.NewFakeFileExec()), modschema.DecodeOptions{})
	bad := []string{"1\nkernel.sysrq = 1", "1\r", "1\x00", "1\t"}
	for _, v := range bad {
		_, err := build("net.ipv4.ip_forward", map[string]any{"value": v})
		if err == nil {
			t.Errorf("value %q: expected builder error, got nil", v)
			continue
		}
		if !strings.Contains(err.Error(), "value:") {
			t.Errorf("value %q: error %q should name the value parameter", v, err)
		}
	}
	// Multi-token values (e.g. net.ipv4.ip_local_port_range) have interior
	// spaces — those are legitimate and must pass.
	for _, v := range []string{"1", "32768 60999", "4096 87380 6291456", "0"} {
		if _, err := build("net.ipv4.ip_local_port_range", map[string]any{"value": v}); err != nil {
			t.Errorf("value %q: unexpected builder error %v", v, err)
		}
	}
}
