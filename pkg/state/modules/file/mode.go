package filemod

import (
	"fmt"
	"io/fs"
	"strconv"
)

// parseFileMode converts an octal mode string — possibly carrying
// setuid/setgid/sticky digits, e.g. "4755", "2755", "1777" — into an
// fs.FileMode. Go represents the special bits as ModeSetuid/ModeSetgid/
// ModeSticky FLAGS, not the raw octal 4000/2000/1000 bits: passing
// fs.FileMode(0o4755) to Chmod silently drops the setuid bit (and the state
// would re-apply forever without ever setting it).
func parseFileMode(s string) (fs.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, err
	}
	if v > 0o7777 {
		return 0, fmt.Errorf("mode %q out of range", s)
	}
	m := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m, nil
}

// modeSpecial are the non-permission mode bits states manage.
const modeSpecial = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// modesEqual reports whether two modes agree on the managed facets:
// permission bits plus setuid/setgid/sticky. File-type bits are ignored.
func modesEqual(a, b fs.FileMode) bool {
	return a.Perm() == b.Perm() && a&modeSpecial == b&modeSpecial
}

// octalMode renders the managed mode facets in familiar octal ("4755").
func octalMode(m fs.FileMode) string {
	v := uint64(m.Perm())
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return strconv.FormatUint(v, 8)
}
