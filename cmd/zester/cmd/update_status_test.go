package cmd

import "testing"

func TestYesNo(t *testing.T) {
	if got := yesNo(true); got != "yes" {
		t.Errorf("yesNo(true) = %q, want yes", got)
	}
	if got := yesNo(false); got != "no" {
		t.Errorf("yesNo(false) = %q, want no", got)
	}
}

func TestProtocolCol(t *testing.T) {
	cases := map[int]string{
		0:  "-", // legacy watchdog, no protocol reported
		-1: "-",
		1:  "1",
		42: "42",
	}
	for in, want := range cases {
		if got := protocolCol(in); got != want {
			t.Errorf("protocolCol(%d) = %q, want %q", in, got, want)
		}
	}
}
