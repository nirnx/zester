package cmd

import (
	"fmt"

	"github.com/nirnx/zester/pkg/job"
)

// Peel-result exit codes (documented in the CLI reference). 0 = every
// targeted peel returned success; 1 stays the generic CLI/infrastructure
// error (bad usage, no master, resolve failure). The classified codes make
// job outcomes reliable for scripts and monitoring:
const (
	// ExitPeelFailures: one or more peels RETURNED a failed result
	// (execution failure), everything was reachable.
	ExitPeelFailures = 2
	// ExitUnreachable: one or more peels were UNREACHABLE (synthetic
	// unreachable return, or missing entirely at timeout); every peel that
	// did return succeeded.
	ExitUnreachable = 3
	// ExitFailuresAndUnreachable: both classes at once — execution
	// failures AND unreachable/missing targets (the combined code, so
	// neither class is masked by a precedence rule).
	ExitFailuresAndUnreachable = 4
)

// ExitCodeError carries a classified process exit code to main. Its message
// is the one-line summary printed to stderr.
type ExitCodeError struct {
	Code int
	Msg  string
}

func (e *ExitCodeError) Error() string { return e.Msg }

// classifyReturns folds a job's collected returns — plus targets that never
// returned at all — into the documented exit classification.
func classifyReturns(returns []job.Return, targets int) error {
	failed, unreachable := 0, 0
	for _, ret := range returns {
		switch {
		case ret.Unreachable:
			unreachable++
		case !ret.Success || ret.Error != "":
			failed++
		}
	}
	if missing := targets - len(returns); missing > 0 {
		unreachable += missing
	}
	return classifyPeelResults(failed, unreachable)
}

// classifyPeelResults maps per-peel outcome counts to the documented exit
// codes. failed counts peels that returned an execution failure; unreachable
// counts peels with a synthetic UNREACHABLE return or no return at all.
// Returns nil when everything succeeded.
func classifyPeelResults(failed, unreachable int) error {
	switch {
	case failed > 0 && unreachable > 0:
		return &ExitCodeError{Code: ExitFailuresAndUnreachable,
			Msg: fmt.Sprintf("%d peel(s) failed, %d unreachable", failed, unreachable)}
	case unreachable > 0:
		return &ExitCodeError{Code: ExitUnreachable,
			Msg: fmt.Sprintf("%d peel(s) unreachable or missing", unreachable)}
	case failed > 0:
		return &ExitCodeError{Code: ExitPeelFailures,
			Msg: fmt.Sprintf("%d peel(s) returned a failed result", failed)}
	default:
		return nil
	}
}
