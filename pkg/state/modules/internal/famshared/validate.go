package famshared

import (
	"fmt"
	"unicode"
)

// This file holds the parameter-value validators shared by the modules that
// rewrite line- or whitespace-delimited system files from state parameters
// (authorized_keys, /etc/hosts, crontabs, the sysctl drop-in, fstab, apt/yum
// repo definitions). Those modules splice a parameter's value into the file
// VERBATIM, so a value carrying an embedded newline injects an extra,
// unmanaged line under the state's identity — a second SSH key, host mapping,
// or cron job — and a stray space inside a single-column field shifts every
// following column. A value that reaches a state from facts (another peel's
// grains via basket()) is untrusted input; rejecting it in the builder tail
// keeps the file format intact and fails the build with a pointed error
// instead of writing a corrupted system file. famshared is the right home
// (keystone spec §13): these are used by more than one family package.

// NoControlChars returns an error when value contains a control rune — \n,
// \r, \t, NUL, DEL, the C1 range (anything unicode.IsControl). field names
// the parameter in the message; the value itself is not echoed (it may be
// long, like a key blob).
func NoControlChars(field, value string) error {
	for i, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s: control character %s at byte %d is not allowed", field, describeRune(r), i)
		}
	}
	return nil
}

// NoWhitespace is NoControlChars plus any Unicode whitespace: for values that
// are a single whitespace-delimited FIELD of a system file (a hosts-file
// name, an fstab column, a cron schedule field, an SSH key type), where a
// space would split one field into two.
func NoWhitespace(field, value string) error {
	for i, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%s: whitespace or control character %s at byte %d is not allowed", field, describeRune(r), i)
		}
	}
	return nil
}

// describeRune renders a rune for a diagnostic as its quoted Go literal plus
// code point, e.g. `'\n' (U+000A)`.
func describeRune(r rune) string {
	return fmt.Sprintf("%q (U+%04X)", r, r)
}
