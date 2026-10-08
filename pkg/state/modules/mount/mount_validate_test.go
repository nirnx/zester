package mountmod

import (
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

// TestMountMountedRejectsWhitespaceColumns pins the builder-tail validation:
// name, device, fstype, and opts are each ONE whitespace-delimited fstab
// column written verbatim, so a space shifts the following columns and a
// newline appends an extra, unmanaged fstab entry.
func TestMountMountedRejectsWhitespaceColumns(t *testing.T) {
	build := NewMountMountedBuilder(testMountMctx(exectest.NewFakeMountExec()), modschema.DecodeOptions{})
	base := func(over map[string]any) map[string]any {
		cfg := map[string]any{"device": "/dev/sdb1"}
		for k, v := range over {
			cfg[k] = v
		}
		return cfg
	}

	bad := []struct {
		name   string
		id     string
		config map[string]any
		field  string
	}{
		{"space in mount point", "/mnt/my data", base(nil), "name"},
		{"newline in mount point", "/mnt/data\n/dev/evil /mnt/evil ext4 defaults 0 0", base(nil), "name"},
		{"space in device", "/mnt/data", base(map[string]any{"device": "/dev/sdb1 /mnt/evil"}), "device"},
		{"newline in device", "/mnt/data", base(map[string]any{"device": "/dev/sdb1\n"}), "device"},
		{"space in fstype", "/mnt/data", base(map[string]any{"fstype": "ext4 rw"}), "fstype"},
		{"space in opts", "/mnt/data", base(map[string]any{"opts": "defaults, noatime"}), "opts"},
		{"newline in opts", "/mnt/data", base(map[string]any{"opts": "defaults\n/dev/evil /mnt/evil ext4 defaults 0 0"}), "opts"},
		{"tab in opts", "/mnt/data", base(map[string]any{"opts": "defaults\tnoatime"}), "opts"},
	}
	for _, tc := range bad {
		_, err := build(tc.id, tc.config)
		if err == nil {
			t.Errorf("%s: expected builder error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.field+":") {
			t.Errorf("%s: error %q should name the %q parameter", tc.name, err, tc.field)
		}
	}
}

func TestMountMountedAcceptsOrdinaryValues(t *testing.T) {
	build := NewMountMountedBuilder(testMountMctx(exectest.NewFakeMountExec()), modschema.DecodeOptions{})
	good := []struct {
		id     string
		config map[string]any
	}{
		{"/mnt/data", map[string]any{"device": "/dev/sdb1"}},
		{"/mnt/data", map[string]any{"device": "UUID=0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", "fstype": "xfs", "opts": "defaults,noatime,nofail"}},
		{"/mnt/share", map[string]any{"device": "10.0.0.5:/vol", "fstype": "nfs", "opts": "defaults,nofail,_netdev", "pass": 2}},
		{"/mnt/data", map[string]any{"device": "LABEL=data", "opts": "rw,relatime,data=ordered"}},
	}
	for _, tc := range good {
		if _, err := build(tc.id, tc.config); err != nil {
			t.Errorf("id %q config %v: unexpected builder error %v", tc.id, tc.config, err)
		}
	}
}
