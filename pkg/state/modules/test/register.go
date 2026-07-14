// Package testmod implements the test.* state-module family.
package testmod

import "github.com/nirnx/zester/pkg/state/modules/regdef"

// Rows is the test.* family's slice of the built-in state-module table, in the
// family's historical registration order. The aggregator (pkg/state/modules)
// concatenates every family's Rows into the full table.
var Rows = []regdef.Registration{
	// test.* — self-documenting schema (the final wave; BuildPlain factories:
	// no exec providers, but they thread the decode policy). test.ping/test.nop
	// declare no parameters; test.fail_without_changes/test.succeed_with_changes
	// carry a `comment` string; test.configurable_test_state carries eager
	// default=true `result`/`changes` bools (the BD-2/BD-7 activation) plus a
	// `comment`.
	{Name: "test.ping", Spec: testPingSpec, BuildPlain: NewTestPingBuilder},
	{Name: "test.nop", Spec: testNopSpec, BuildPlain: NewTestNopBuilder},
	{Name: "test.fail_without_changes", Spec: testFailWithoutChangesSpec, BuildPlain: NewTestFailWithoutChangesBuilder},
	{Name: "test.succeed_with_changes", Spec: testSucceedWithChangesSpec, BuildPlain: NewTestSucceedWithChangesBuilder},
	{Name: "test.configurable_test_state", Spec: testConfigurableTestStateSpec, BuildPlain: NewTestConfigurableTestStateBuilder},
}
