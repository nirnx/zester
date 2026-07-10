package update

import (
	"strconv"
	"strings"
)

// CompareVersions orders two version strings numerically per dotted segment
// ("0.4.10" > "0.4.2"), tolerating a leading "v" and comparing any
// non-numeric tail lexically. Returns -1, 0, or 1. Used by auto-rollout to
// pick the latest promoted version and to refuse downgrades — NOT a full
// semver implementation (no pre-release precedence), which matches zester's
// plain x.y.z release scheme.
func CompareVersions(a, b string) int {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		default:
			if av != bv {
				if av < bv {
					return -1
				}
				return 1
			}
		}
	}
	return 0
}
