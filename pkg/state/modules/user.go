package modules

// This file holds the shared slice helpers used across the user, group, and
// host module families. The user.present module lives in user_present.go and
// user.absent in user_absent.go; keeping the small, dependency-free helpers here
// (rather than duplicating them per module) mirrors how git.go retains the
// shared rev-comparison helpers after the git.cloned/git.latest split.

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
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
