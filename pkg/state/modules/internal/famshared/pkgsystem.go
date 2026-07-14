package famshared

import "strings"

// This file holds the package-system classification shared by the pkg.* and
// pkgrepo.* module families (pkg.latest/pkg.purged shell out to the manager
// CLI; pkgrepo.managed picks the repo-file layout from the family).

// pkgFactFamily returns the lowercased os.family fact, or "" when absent.
func pkgFactFamily(facts map[string]any) string {
	m, ok := facts["os"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["family"].(string)
	return strings.ToLower(strings.TrimSpace(s))
}

// DetectPkgSystem classifies the host package system from facts (preferred)
// and the active package provider name (fallback). It returns a normalized
// family ("debian", "redhat", "darwin", or "") and the package manager CLI
// to shell out to ("apt-get", "dnf", "yum", "brew", or "").
func DetectPkgSystem(facts map[string]any, providerName string) (family, mgr string) {
	switch pkgFactFamily(facts) {
	case "debian", "ubuntu":
		return "debian", "apt-get"
	case "redhat", "rhel", "fedora", "centos", "suse", "opensuse":
		if providerName == "yum" {
			return "redhat", "yum"
		}
		return "redhat", "dnf"
	case "darwin", "macos":
		return "darwin", "brew"
	}

	// Fall back to the provider name when facts are unavailable.
	switch providerName {
	case "apt":
		return "debian", "apt-get"
	case "dnf":
		return "redhat", "dnf"
	case "yum":
		return "redhat", "yum"
	case "brew":
		return "darwin", "brew"
	}

	return "", ""
}
