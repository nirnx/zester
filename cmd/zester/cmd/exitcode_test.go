package cmd

import (
	"errors"
	"testing"

	"github.com/nirnx/zester/pkg/job"
)

// The documented exit-code contract: 0 all-success, 2 execution failures,
// 3 unreachable/missing, 4 both (the combined code — neither class masked).
func TestClassifyPeelResults(t *testing.T) {
	tests := []struct {
		name        string
		failed      int
		unreachable int
		wantCode    int // 0 = nil error
	}{
		{"all success", 0, 0, 0},
		{"failures only", 2, 0, ExitPeelFailures},
		{"unreachable only", 0, 1, ExitUnreachable},
		{"both", 1, 1, ExitFailuresAndUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyPeelResults(tt.failed, tt.unreachable)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("classify(%d,%d) = %v, want nil", tt.failed, tt.unreachable, err)
				}
				return
			}
			var ec *ExitCodeError
			if !errors.As(err, &ec) || ec.Code != tt.wantCode {
				t.Fatalf("classify(%d,%d) = %v, want ExitCodeError code %d", tt.failed, tt.unreachable, err, tt.wantCode)
			}
		})
	}
}

// classifyReturns folds returns and missing targets: an UNREACHABLE synthetic
// return and a target that never returned both land in the unreachable class;
// a returned failure is the execution-failure class.
func TestClassifyReturns(t *testing.T) {
	ok := job.Return{PeelID: "a", Success: true}
	fail := job.Return{PeelID: "b", Success: false, Error: "exit 1"}
	unreach := job.Return{PeelID: "c", Unreachable: true, Error: job.UnreachableError}

	if err := classifyReturns([]job.Return{ok, ok}, 2); err != nil {
		t.Errorf("all success = %v, want nil", err)
	}

	var ec *ExitCodeError
	if err := classifyReturns([]job.Return{ok, fail}, 2); !errors.As(err, &ec) || ec.Code != ExitPeelFailures {
		t.Errorf("one failure = %v, want code %d", err, ExitPeelFailures)
	}
	if err := classifyReturns([]job.Return{ok, unreach}, 2); !errors.As(err, &ec) || ec.Code != ExitUnreachable {
		t.Errorf("one unreachable = %v, want code %d", err, ExitUnreachable)
	}
	// A target missing entirely at timeout counts as unreachable.
	if err := classifyReturns([]job.Return{ok}, 2); !errors.As(err, &ec) || ec.Code != ExitUnreachable {
		t.Errorf("one missing = %v, want code %d", err, ExitUnreachable)
	}
	if err := classifyReturns([]job.Return{fail, unreach}, 3); !errors.As(err, &ec) || ec.Code != ExitFailuresAndUnreachable {
		t.Errorf("failure + unreachable + missing = %v, want code %d", err, ExitFailuresAndUnreachable)
	}
}

// The JSON/YAML record status vocabulary scripts key on.
func TestJobReturnsToOutputRecordsStatus(t *testing.T) {
	records := jobReturnsToOutputRecords([]job.Return{
		{PeelID: "a", Success: true},
		{PeelID: "b", Success: false, Error: "exit 1"},
		{PeelID: "c", Unreachable: true, Error: job.UnreachableError},
	})
	want := map[string]string{"a": "success", "b": "failed", "c": "unreachable"}
	for _, rec := range records {
		if rec.Status != want[rec.PeelID] {
			t.Errorf("record %s status = %q, want %q", rec.PeelID, rec.Status, want[rec.PeelID])
		}
	}
}
