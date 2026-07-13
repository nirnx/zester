package execmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
)

// This file gives every built-in remote-execution function a self-documenting
// modschema.Spec (keystone spec §4/§5): a compiled parameter schema plus
// documentation metadata, registered via Registry.RegisterSpec so Describe,
// SpecNames, sys.doc, and docgen all read one artifact. Execution functions are
// KindExec — phase-less imperative surfaces, so their Effects populate only
// Execution (never Check/Apply/Revert; ValidateEffects enforces that).
//
// The parameter protos below are derived from the ACTUAL argStr alias sets at
// each call site in builtins.go / sysdoc.go: the FIRST argStr key becomes the
// primary canonical name, the remaining keys (except the "__id__" request-ID
// fallback, which the `primary` option models) become aliases, in order. So
// `pkgVersion`'s `argStr(args, "name", "package", "pkg", "__id__")` maps to a
// `name` primary with `package`/`pkg` aliases. The functions themselves still
// resolve arguments through argStr at runtime — these specs are the documentation
// and schema surface (and a permanent contract-fixture target), kept faithful to
// the code they describe.

// execNoParams is the shared proto for a parameterless execution function
// (test.version, test.true, test.false, pkg.list_pkgs, grains.items,
// sys.list_functions): its compiled schema records zero fields.
type execNoParams struct{}

// echoParams is test.echo's schema proto: argStr(args, "text", "name", "__id__").
type echoParams struct {
	Text string `zester:"text,primary,aliases=name" usage:"text to echo back; defaults to the request ID"`
}

// pkgNameParams is pkg.version's schema proto:
// argStr(args, "name", "package", "pkg", "__id__").
type pkgNameParams struct {
	Name string `zester:"name,primary,aliases=package|pkg" usage:"package name to query; defaults to the request ID"`
}

// serviceNameParams is the service.* query/action schema proto:
// argStr(args, "name", "service", "__id__").
type serviceNameParams struct {
	Name string `zester:"name,primary,aliases=service" usage:"service name; defaults to the request ID"`
}

// diskUsageParams is disk.usage's schema proto: argStr(args, "path", "name",
// "__id__"). The path is optional — when neither it nor the request ID is
// supplied, every mounted filesystem is reported.
type diskUsageParams struct {
	Path string `zester:"path,primary,aliases=name" usage:"filesystem path to report usage for; defaults to the request ID, and when neither is given every mounted filesystem is reported"`
}

// cmdRunExecParams is the cmd.run EXECUTION-module schema proto:
// command := argStr(args, "cmd", "command", "name", "__id__") and
// opts.Dir = argStr(args, "cwd", "dir"). M3 (keystone spec final fix pass): the
// canonical/alias ORDER must mirror argStr's own source-precedence order
// exactly, because modschema's source resolution checks the canonical name
// first, then aliases left-to-right — a declared `command,aliases=cmd|name`
// would resolve `command` before `cmd` even though argStr checks `cmd` FIRST,
// so a caller setting both would silently get the WRONG one accepted by the
// spec relative to what the runtime actually executes. `cmd` is therefore the
// canonical primary (aliases `command`, `name`, in argStr's exact order) — this
// deliberately does NOT match the cmd.run STATE module's primary (`command`,
// keystone spec §3/BD-8); the two are different surfaces reading a different
// runtime precedence, and this exec spec exists purely for coverage/consistency
// (not rendered as its own page — docgen documents cmd.run via the state
// page's dual-surface appendix).
type cmdRunExecParams struct {
	Command string `zester:"cmd,primary,aliases=command|name" usage:"command line to run through the shell (sh -c); defaults to the request ID"`
	Cwd     string `zester:"cwd,aliases=dir" usage:"working directory for the command; defaults to the peel process's working directory"`
}

// grainsItemParams is grains.item's schema proto:
// argStr(args, "key", "name", "__id__").
type grainsItemParams struct {
	Key string `zester:"key,primary,aliases=name" usage:"dotted grain (fact) key to look up (e.g. os.family); defaults to the request ID"`
}

// sysDocParams is sys.doc's schema proto: argStr(args, "name", "module",
// "__id__"). With no module named, sys.doc returns the unified index.
type sysDocParams struct {
	Name string `zester:"name,primary,aliases=module" usage:"module name — or bare family name, rendering every member — to document; when omitted, the unified index of every callable surface is returned"`
}

// mustExecSpec compiles an execution-module spec at package init, panicking on a
// compile error — an invalid schema declaration is a programming error caught at
// load, never a runtime condition. It mirrors pkg/state/modules' mustSpec.
func mustExecSpec(module string, proto any, doc modschema.Doc) *modschema.Spec {
	s, err := modschema.NewSpec(module, modschema.KindExec, proto, doc)
	if err != nil {
		panic(fmt.Sprintf("execmod: spec %s: %v", module, err))
	}
	return s
}

// --- test.* -----------------------------------------------------------------

var testEchoSpec = mustExecSpec("test.echo", echoParams{}, modschema.Doc{
	Summary: "Echo the supplied text back unchanged.",
	Description: "`test.echo` returns its `text` argument verbatim — a trivial round-trip diagnostic. " +
		"With no `text` (or `name`) argument it echoes the request ID, so a bare positional is echoed as-is.",
	Effects: modschema.Effects{
		Execution: "Returns the `text` argument (falling back to `name`, then the request ID) unchanged. " +
			"It touches no providers and never fails.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Echo a string",
			Kind:        "cli",
			Explanation: "The bare positional argument is echoed back.",
			Code:        "zester '*' test.echo hello",
		},
	},
})

var testVersionSpec = mustExecSpec("test.version", execNoParams{}, modschema.Doc{
	Summary: "Report the peel's Zester build version.",
	Description: "`test.version` returns the running peel binary's Zester version string " +
		"(the same value injected at build time via `internal/version`). It takes no arguments.",
	Effects: modschema.Effects{
		Execution: "Returns the peel's compiled-in version string. It touches no providers and never fails.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Report the version of every peel",
			Kind:        "cli",
			Explanation: "Useful for confirming a fleet is on the expected build.",
			Code:        "zester '*' test.version",
		},
	},
})

var testTrueSpec = mustExecSpec("test.true", execNoParams{}, modschema.Doc{
	Summary: "Always return the string \"true\".",
	Description: "`test.true` unconditionally returns `\"true\"`. It takes no arguments and is a " +
		"companion to `test.false` for exercising success paths and pipelines.",
	Effects: modschema.Effects{
		Execution: "Returns the literal string \"true\". It touches no providers and never fails.",
	},
	Examples: []modschema.Example{
		{
			Title: "Return true",
			Kind:  "cli",
			Code:  "zester '*' test.true",
		},
	},
	SeeAlso: []string{"test.false"},
})

var testFalseSpec = mustExecSpec("test.false", execNoParams{}, modschema.Doc{
	Summary: "Always return the string \"false\".",
	Description: "`test.false` unconditionally returns `\"false\"`. It takes no arguments and is a " +
		"companion to `test.true`. It returns a string, not a non-zero exit — the call itself still succeeds.",
	Effects: modschema.Effects{
		Execution: "Returns the literal string \"false\". It touches no providers and never fails.",
	},
	Examples: []modschema.Example{
		{
			Title: "Return false",
			Kind:  "cli",
			Code:  "zester '*' test.false",
		},
	},
	SeeAlso: []string{"test.true"},
})

// --- pkg.* ------------------------------------------------------------------

var pkgVersionSpec = mustExecSpec("pkg.version", pkgNameParams{}, modschema.Doc{
	Summary: "Report the installed version of a package.",
	Description: "`pkg.version` queries the installed version of the named package through the host's " +
		"detected package manager. The package name defaults to the request ID, so `zester '*' pkg.version " +
		"nginx` reports nginx's version.",
	Effects: modschema.Effects{
		Execution: "Probes the installed version per detected manager: apt via `dpkg-query -W -f='${Version}'`, " +
			"dnf/yum via `rpm -q --qf '%{VERSION}-%{RELEASE}'`, brew via `brew list --versions`. When no package " +
			"provider is detected it falls back to probing dpkg then rpm in turn. Returns the trimmed version " +
			"string; errors when no package name (or request ID) is given, when no command provider is available, " +
			"or when the package is not installed.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Query a package version",
			Kind:        "cli",
			Explanation: "The bare positional argument is the package name.",
			Code:        "zester '*' pkg.version nginx",
		},
	},
	SeeAlso: []string{"pkg.installed"},
})

var pkgListPkgsSpec = mustExecSpec("pkg.list_pkgs", execNoParams{}, modschema.Doc{
	Summary: "List every installed package and its version.",
	Description: "`pkg.list_pkgs` returns one `name version` line per installed package, queried through the " +
		"host's detected package manager. It takes no arguments.",
	Effects: modschema.Effects{
		Execution: "Lists installed packages per detected manager: apt via `dpkg-query -W`, dnf/yum via " +
			"`rpm -qa`, brew via `brew list --versions`; with no detected provider it falls back to probing dpkg " +
			"then rpm. Returns the package listing (trailing newline trimmed); errors when no command provider is " +
			"available.",
	},
	Examples: []modschema.Example{
		{
			Title: "List installed packages",
			Kind:  "cli",
			Code:  "zester 'web-01' pkg.list_pkgs",
		},
	},
	SeeAlso: []string{"pkg.installed"},
})

// --- service.* --------------------------------------------------------------

var serviceStatusSpec = mustExecSpec("service.status", serviceNameParams{}, modschema.Doc{
	Summary: "Report whether a service is running.",
	Description: "`service.status` reports the running state of the named service (defaulting to the request " +
		"ID). Through the service provider it returns `running` or `stopped`; when only a command provider is " +
		"available it falls back to `systemctl is-active` and returns that word (or `unknown`).",
	Effects: modschema.Effects{
		Execution: "Queries the service provider's running state and returns `running` or `stopped`. With no " +
			"service provider it runs `systemctl is-active <name>` and returns the reported word (`unknown` when " +
			"empty). Errors when no service name (or request ID) is given, or when neither a service nor a command " +
			"provider is available.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Check a service's status",
			Kind:        "cli",
			Explanation: "The bare positional argument is the service name.",
			Code:        "zester 'web-01' service.status nginx",
		},
	},
	SeeAlso: []string{"service.running"},
})

var serviceStartSpec = mustExecSpec("service.start", serviceNameParams{}, modschema.Doc{
	Summary: "Start a service.",
	Description: "`service.start` starts the named service (defaulting to the request ID) through the service " +
		"provider, falling back to `systemctl start` when only a command provider is available. Unlike " +
		"`service.running`, it is imperative and non-idempotent — it always issues the start.",
	Effects: modschema.Effects{
		Execution: "Starts the service via the service provider (or `systemctl start <name>` as a fallback) and " +
			"returns `started <name>`. Errors when no service name (or request ID) is given, when neither provider " +
			"is available, or when the start command exits non-zero.",
	},
	Examples: []modschema.Example{
		{
			Title: "Start a service",
			Kind:  "cli",
			Code:  "zester 'web-01' service.start nginx",
		},
	},
	SeeAlso: []string{"service.running"},
})

var serviceStopSpec = mustExecSpec("service.stop", serviceNameParams{}, modschema.Doc{
	Summary: "Stop a service.",
	Description: "`service.stop` stops the named service (defaulting to the request ID) through the service " +
		"provider, falling back to `systemctl stop` when only a command provider is available. It is imperative " +
		"and non-idempotent — the counterpart of `service.dead` for ad-hoc use.",
	Effects: modschema.Effects{
		Execution: "Stops the service via the service provider (or `systemctl stop <name>` as a fallback) and " +
			"returns `stopped <name>`. Errors when no service name (or request ID) is given, when neither provider " +
			"is available, or when the stop command exits non-zero.",
	},
	Examples: []modschema.Example{
		{
			Title: "Stop a service",
			Kind:  "cli",
			Code:  "zester 'web-01' service.stop nginx",
		},
	},
	SeeAlso: []string{"service.dead"},
})

var serviceRestartSpec = mustExecSpec("service.restart", serviceNameParams{}, modschema.Doc{
	Summary: "Restart a service.",
	Description: "`service.restart` restarts the named service (defaulting to the request ID) through the " +
		"service provider, falling back to `systemctl restart` when only a command provider is available.",
	Effects: modschema.Effects{
		Execution: "Restarts the service via the service provider (or `systemctl restart <name>` as a fallback) " +
			"and returns `restarted <name>`. Errors when no service name (or request ID) is given, when neither " +
			"provider is available, or when the restart command exits non-zero.",
	},
	Examples: []modschema.Example{
		{
			Title: "Restart a service",
			Kind:  "cli",
			Code:  "zester 'web-01' service.restart nginx",
		},
	},
	SeeAlso: []string{"service.running"},
})

// --- disk.* -----------------------------------------------------------------

var diskUsageSpec = mustExecSpec("disk.usage", diskUsageParams{}, modschema.Doc{
	Summary: "Report filesystem disk usage.",
	Description: "`disk.usage` returns portable `df -P` output. With a `path` argument (or a request ID) it " +
		"reports the filesystem containing that path; with neither, it reports every mounted filesystem.",
	Effects: modschema.Effects{
		Execution: "Runs `df -P` (appending the shell-quoted `path` when given) through the command provider " +
			"and returns its output with the trailing newline trimmed. Errors when no command provider is available.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Report usage for all filesystems",
			Kind:        "cli",
			Explanation: "With no path, every mounted filesystem is reported.",
			Code:        "zester '*' disk.usage",
		},
		{
			Title:       "Report usage for one path",
			Kind:        "cli",
			Explanation: "The bare positional argument is the path to report.",
			Code:        "zester 'web-01' disk.usage /var",
		},
	},
})

// --- cmd.run (execution surface; documented via the cmd.run state page) ------

var cmdRunExecSpec = mustExecSpec("cmd.run", cmdRunExecParams{}, modschema.Doc{
	Summary: "Run a command and capture its output (execution surface).",
	Description: "`cmd.run` as an execution module runs `command` through the shell (`sh -c`) in the optional " +
		"`cwd` and returns its stdout. It is the imperative sibling of the `cmd.run` STATE module — reachable from " +
		"templates as `salt['cmd.run'](...)` and from the CLI as `zester '<target>' cmd.run '<command>'`. The " +
		"state module (with `creates` idempotency and the Check/Apply/Revert lifecycle) is the primary, documented " +
		"surface.",
	Effects: modschema.Effects{
		Execution: "Runs `command` via `sh -c` in `cwd` (the peel's working directory when unset) through the " +
			"command provider and returns stdout with the trailing newline trimmed. Errors when no command (or " +
			"request ID) is given, when no command provider is available, or when the command itself fails.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Run a command ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the command string.",
			Code:        "zester 'web*' cmd.run 'df -h'",
		},
	},
	SeeAlso: []string{"cmd.run"},
})

// --- grains.* ---------------------------------------------------------------

var grainsItemSpec = mustExecSpec("grains.item", grainsItemParams{}, modschema.Doc{
	Summary: "Look up a single fact (grain) by dotted key.",
	Description: "`grains.item` resolves a dotted fact key (Salt calls facts \"grains\") against the peel's " +
		"in-memory facts and returns its value. The key defaults to the request ID. A missing key returns an " +
		"empty string, not an error.",
	Effects: modschema.Effects{
		Execution: "Resolves the dotted `key` against the peel's facts map (segment by segment) and returns the " +
			"value — scalars directly, composite values as trimmed YAML. A missing key or segment returns the empty " +
			"string. Errors only when no key (or request ID) is given.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Read a nested fact",
			Kind:        "cli",
			Explanation: "The bare positional argument is the dotted fact key.",
			Code:        "zester '*' grains.item os.family",
		},
	},
	SeeAlso: []string{"grains.items"},
})

var grainsItemsSpec = mustExecSpec("grains.items", execNoParams{}, modschema.Doc{
	Summary: "Return all facts (grains) for the peel.",
	Description: "`grains.items` returns the peel's entire in-memory facts map rendered as YAML. It takes no " +
		"arguments.",
	Effects: modschema.Effects{
		Execution: "Renders the peel's full facts map as trimmed YAML and returns it. Returns the empty string " +
			"when no facts are available; it never fails.",
	},
	Examples: []modschema.Example{
		{
			Title: "Dump every fact",
			Kind:  "cli",
			Code:  "zester 'web-01' grains.items",
		},
	},
	SeeAlso: []string{"grains.item"},
})

// --- sys.* ------------------------------------------------------------------

var sysListFunctionsSpec = mustExecSpec("sys.list_functions", execNoParams{}, modschema.Doc{
	Summary: "List every callable module and function on the peel.",
	Description: "`sys.list_functions` returns the sorted names of every callable surface — state modules, " +
		"execution functions, and the peel's dispatch specials (facts.*, settings.*, …) — one per line. It takes " +
		"no arguments.",
	Effects: modschema.Effects{
		Execution: "Returns the sorted, newline-joined names of every callable surface the peel exposes. It " +
			"touches no providers and never fails.",
	},
	Examples: []modschema.Example{
		{
			Title: "List callable functions",
			Kind:  "cli",
			Code:  "zester 'web-01' sys.list_functions",
		},
	},
	SeeAlso: []string{"sys.doc"},
})

var sysDocSpec = mustExecSpec("sys.doc", sysDocParams{}, modschema.Doc{
	Summary: "Render a module's documentation, or the unified index.",
	Description: "`sys.doc <module>` renders the named module's documentation — the SAME self-documenting " +
		"metadata `zester doc` and the generated reference pages are built from — as plain text. A bare FAMILY " +
		"name renders every documented member (`sys.doc ssh_auth` shows ssh_auth.present AND ssh_auth.absent; " +
		"Salt parity). With no module named, it returns the unified index of every callable surface (identical " +
		"to `sys.list_functions`). It answers even during a long-running state run (it is a read-only surface).",
	Effects: modschema.Effects{
		Execution: "Looks the module up following the peel's dispatch precedence (dispatch specials, then state " +
			"modules, then execution functions) and renders its ModuleInfo through the shared text renderer; a " +
			"name matching no module but prefixing a family renders every `<family>.*` member as one document. " +
			"With no module named, returns the unified index. Errors when the name is neither a module nor a " +
			"family.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Document a module",
			Kind:        "cli",
			Explanation: "The bare positional argument is the module name to document.",
			Code:        "zester 'web-01' sys.doc pkg.installed",
		},
		{
			Title:       "Document a whole family",
			Kind:        "cli",
			Explanation: "A bare family name renders every documented member.",
			Code:        "zester 'web-01' sys.doc ssh_auth",
		},
		{
			Title:       "Show the unified index",
			Kind:        "cli",
			Explanation: "With no module named, sys.doc lists every callable surface.",
			Code:        "zester 'web-01' sys.doc",
		},
	},
	SeeAlso: []string{"sys.list_functions"},
})

// registerBuiltinSpecs registers every built-in execution function into r
// together with its self-documenting spec (RegisterSpec), replacing the plain
// Register calls DefaultRegistry used before self-documenting modules. A
// RegisterSpec failure here (nil/empty/duplicate) is a programming error and
// panics at load, exactly like the state registry's RegisterAll.
//
// sys.list_functions and sys.doc are registered with their specs but a
// registry-local function: sys.list_functions lists THIS registry's names (the
// DefaultRegistry fallback), and sys.doc uses a nil DocSource placeholder that
// errors if called on a bare registry. The peel swaps both functions for their
// cross-surface DocSource-backed variants (internal/peeld.wireDocSource), which
// preserves these specs (a plain Register replaces only the function).
func registerBuiltinSpecs(r *Registry) {
	specFns := []struct {
		spec *modschema.Spec
		fn   Func
	}{
		{testEchoSpec, testEcho},
		{testVersionSpec, testVersion},
		{testTrueSpec, testTrue},
		{testFalseSpec, testFalse},

		{pkgVersionSpec, pkgVersion},
		{pkgListPkgsSpec, pkgListPkgs},

		{serviceStatusSpec, serviceStatus},
		{serviceStartSpec, serviceStart},
		{serviceStopSpec, serviceStop},
		{serviceRestartSpec, serviceRestart},

		{diskUsageSpec, diskUsage},
		{cmdRunExecSpec, cmdRun},

		{grainsItemSpec, grainsItem},
		{grainsItemsSpec, grainsItems},

		// sys.list_functions closes over r so its listing reflects everything
		// registered on this instance (the bare-registry fallback).
		{sysListFunctionsSpec, func(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
			return strings.Join(r.Names(), "\n"), nil
		}},
		// sys.doc needs a DocSource; a bare registry has none. The placeholder
		// errors when called, and the peel replaces it via wireDocSource.
		{sysDocSpec, SysDoc(nil)},
	}
	for _, sf := range specFns {
		if err := r.RegisterSpec(sf.spec, sf.fn); err != nil {
			panic(fmt.Sprintf("execmod: register builtin spec %s: %v", sf.spec.Module, err))
		}
	}
}
