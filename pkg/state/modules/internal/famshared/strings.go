package famshared

// This file holds the shared slice helpers used across the user, group, and
// host module families — cross-family helpers live here (keystone spec §13:
// famshared is for helpers used by MORE THAN ONE family package; a helper
// used by a single family stays unexported inside that family).

import "slices"

// ContainsString reports whether ss contains s.
func ContainsString(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

// StringSliceEqual reports whether a and b hold the same elements with the
// same multiplicities, regardless of order.
func StringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int)
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}
