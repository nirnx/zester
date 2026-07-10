//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestPkgLatestSeesReleaseNewerThanAptIndex reproduces the 0.4.1 field bug
// end-to-end: a version published to a repo AFTER the box's last apt-get
// update was invisible to pkg.latest — Check consulted the stale index,
// answered "already latest", and Apply (previously the only refresh site)
// never ran. The state must refresh BEFORE deciding.
//
// Reproduction: a local [trusted=yes] file: repo on db-01 serves
// zester-it-dummy 1.0 (indexed via apt-get update, installed), then gains
// 1.1 with NO apt-get update. pkg.latest must upgrade to 1.1 anyway.
func TestPkgLatestSeesReleaseNewerThanAptIndex(t *testing.T) {
	// dpkg-scanpackages (dpkg-dev) builds the repo's Packages index.
	execInContainer(t, "db-01", []string{"sh", "-c",
		"command -v dpkg-scanpackages >/dev/null || (apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq dpkg-dev)"})

	buildRepo := `
set -e
mkdir -p /opt/itrepo
build() {
  d=/tmp/zester-it-dummy-$1
  mkdir -p "$d/DEBIAN"
  printf 'Package: zester-it-dummy\nVersion: %s\nArchitecture: all\nMaintainer: zester-it <it@zester.cc>\nDescription: pkg.latest stale-index integration dummy\n' "$1" > "$d/DEBIAN/control"
  dpkg-deb --build "$d" "/opt/itrepo/zester-it-dummy_${1}_all.deb" >/dev/null
}
build 1.0
cd /opt/itrepo && dpkg-scanpackages --multiversion . > Packages
echo 'deb [trusted=yes] file:/opt/itrepo ./' > /etc/apt/sources.list.d/zester-it.list
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq zester-it-dummy
`
	execInContainer(t, "db-01", []string{"sh", "-c", buildRepo})
	t.Cleanup(func() {
		execInContainer(t, "db-01", []string{"sh", "-c",
			"DEBIAN_FRONTEND=noninteractive apt-get remove -y -qq zester-it-dummy >/dev/null 2>&1; rm -rf /opt/itrepo /etc/apt/sources.list.d/zester-it.list; apt-get update -qq >/dev/null 2>&1 || true"})
	})

	if v := dummyVersion(t); !strings.HasPrefix(v, "1.0") {
		t.Fatalf("setup: expected zester-it-dummy 1.0 installed, got %q", v)
	}

	// Publish 1.1 to the repo WITHOUT apt-get update: the box's index is now
	// stale relative to the repo — the exact field precondition.
	execInContainer(t, "db-01", []string{"sh", "-c",
		"set -e; d=/tmp/zester-it-dummy-1.1; mkdir -p $d/DEBIAN; printf 'Package: zester-it-dummy\\nVersion: 1.1\\nArchitecture: all\\nMaintainer: zester-it <it@zester.cc>\\nDescription: pkg.latest stale-index integration dummy\\n' > $d/DEBIAN/control; dpkg-deb --build $d /opt/itrepo/zester-it-dummy_1.1_all.deb >/dev/null; cd /opt/itrepo && dpkg-scanpackages --multiversion . > Packages"})

	// pkg.latest must refresh before checking and therefore see + apply 1.1.
	results := execCLI(t, "db-01", "pkg.latest", "zester-it-dummy")
	r := requireSuccess(t, results, "db-01")
	changed := false
	for _, sr := range r.Results {
		if sr.Error != "" {
			t.Fatalf("pkg.latest errored: %s", sr.Error)
		}
		changed = changed || sr.Changed
	}
	if !changed {
		t.Fatal("pkg.latest was a silent no-op on a stale index — Check did not refresh before the upgradability probe")
	}
	if v := dummyVersion(t); !strings.HasPrefix(v, "1.1") {
		t.Fatalf("expected upgrade to 1.1, still at %q", v)
	}
}

func dummyVersion(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(execInContainer(t, "db-01",
		[]string{"sh", "-c", "dpkg-query -W -f '${Version}' zester-it-dummy 2>/dev/null || echo none"}))
}
