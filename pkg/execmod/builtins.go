package execmod

import (
	"context"
	"fmt"
	"strings"

	"github.com/ptorbus/zester/internal/version"
	"github.com/ptorbus/zester/pkg/exec"
	"gopkg.in/yaml.v3"
)

// DefaultRegistry returns a Registry pre-loaded with the built-in starter set
// of remote-execution functions. All functions are implemented against the
// providers on *exec.ModuleContext (Package/Service/Command/File) and its
// Facts map, shelling out via CommandExec where a provider method does not
// already expose the query.
func DefaultRegistry() *Registry {
	r := NewRegistry()

	// test.* — trivial diagnostics.
	r.Register("test.echo", testEcho)
	r.Register("test.version", testVersion)
	r.Register("test.true", testTrue)
	r.Register("test.false", testFalse)

	// pkg.* — package queries.
	r.Register("pkg.version", pkgVersion)
	r.Register("pkg.list_pkgs", pkgListPkgs)

	// service.* — init-system control.
	r.Register("service.status", serviceStatus)
	r.Register("service.start", serviceStart)
	r.Register("service.stop", serviceStop)
	r.Register("service.restart", serviceRestart)

	// disk.* — disk usage.
	r.Register("disk.usage", diskUsage)

	// cmd.* — arbitrary command execution.
	r.Register("cmd.run", cmdRun)

	// grains.* — host facts.
	r.Register("grains.item", grainsItem)
	r.Register("grains.items", grainsItems)

	// sys.* — introspection. Closes over r so the list reflects everything
	// registered above (and any later registrations on this instance).
	r.Register("sys.list_functions", func(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
		return strings.Join(r.Names(), "\n"), nil
	})

	return r
}

// --- test.* -----------------------------------------------------------------

func testEcho(_ context.Context, _ *exec.ModuleContext, args map[string]any) (string, error) {
	return argStr(args, "text", "name", "__id__"), nil
}

func testVersion(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
	return version.Version, nil
}

func testTrue(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
	return "true", nil
}

func testFalse(_ context.Context, _ *exec.ModuleContext, _ map[string]any) (string, error) {
	return "false", nil
}

// --- pkg.* ------------------------------------------------------------------

func pkgVersion(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	name := argStr(args, "name", "package", "pkg", "__id__")
	if name == "" {
		return "", fmt.Errorf("pkg.version: package name required")
	}
	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("pkg.version: no command provider available")
	}

	q := shellQuote(name)
	var cmd string
	switch pkgManager(mctx) {
	case "apt":
		cmd = "dpkg-query -W -f='${Version}' " + q
	case "dnf", "yum":
		cmd = "rpm -q --qf '%{VERSION}-%{RELEASE}' " + q
	case "brew":
		cmd = "brew list --versions " + q
	default:
		// Probe the common managers in turn.
		cmd = "dpkg-query -W -f='${Version}' " + q + " 2>/dev/null || rpm -q --qf '%{VERSION}-%{RELEASE}' " + q + " 2>/dev/null"
	}

	res, err := mctx.Command.Run(ctx, shellCmd(cmd))
	if err != nil {
		return "", fmt.Errorf("pkg.version: query %s: %w", name, err)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return "", fmt.Errorf("pkg.version: %s not installed", name)
	}
	return out, nil
}

func pkgListPkgs(ctx context.Context, mctx *exec.ModuleContext, _ map[string]any) (string, error) {
	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("pkg.list_pkgs: no command provider available")
	}

	var cmd string
	switch pkgManager(mctx) {
	case "apt":
		cmd = `dpkg-query -W -f='${Package} ${Version}\n'`
	case "dnf", "yum":
		cmd = `rpm -qa --qf '%{NAME} %{VERSION}-%{RELEASE}\n'`
	case "brew":
		cmd = "brew list --versions"
	default:
		cmd = `dpkg-query -W -f='${Package} ${Version}\n' 2>/dev/null || rpm -qa --qf '%{NAME} %{VERSION}-%{RELEASE}\n' 2>/dev/null`
	}

	res, err := mctx.Command.Run(ctx, shellCmd(cmd))
	if err != nil {
		return "", fmt.Errorf("pkg.list_pkgs: %w", err)
	}
	return strings.TrimRight(res.Stdout, "\n"), nil
}

// --- service.* --------------------------------------------------------------

func serviceStatus(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	name := argStr(args, "name", "service", "__id__")
	if name == "" {
		return "", fmt.Errorf("service.status: service name required")
	}
	if mctx != nil && mctx.Service != nil {
		running, err := mctx.Service.IsRunning(ctx, name)
		if err != nil {
			return "", fmt.Errorf("service.status: %s: %w", name, err)
		}
		if running {
			return "running", nil
		}
		return "stopped", nil
	}
	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("service.status: no service or command provider available")
	}
	// systemctl is-active exits non-zero when inactive; report its word.
	res, err := mctx.Command.Run(ctx, shellCmd("systemctl is-active "+shellQuote(name)))
	if err != nil && (res == nil || res.Stdout == "") {
		return "", fmt.Errorf("service.status: %s: %w", name, err)
	}
	status := strings.TrimSpace(res.Stdout)
	if status == "" {
		status = "unknown"
	}
	return status, nil
}

func serviceStart(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	return serviceAction(ctx, mctx, args, "start")
}

func serviceStop(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	return serviceAction(ctx, mctx, args, "stop")
}

func serviceRestart(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	return serviceAction(ctx, mctx, args, "restart")
}

// serviceAction performs start/stop/restart via the Service provider when
// available, otherwise shells out to systemctl.
func serviceAction(ctx context.Context, mctx *exec.ModuleContext, args map[string]any, action string) (string, error) {
	name := argStr(args, "name", "service", "__id__")
	if name == "" {
		return "", fmt.Errorf("service.%s: service name required", action)
	}

	past := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[action]

	if mctx != nil && mctx.Service != nil {
		var err error
		switch action {
		case "start":
			err = mctx.Service.Start(ctx, name)
		case "stop":
			err = mctx.Service.Stop(ctx, name)
		case "restart":
			err = mctx.Service.Restart(ctx, name)
		}
		if err != nil {
			return "", fmt.Errorf("service.%s: %s: %w", action, name, err)
		}
		return past + " " + name, nil
	}

	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("service.%s: no service or command provider available", action)
	}
	res, err := mctx.Command.Run(ctx, shellCmd("systemctl "+action+" "+shellQuote(name)))
	if err != nil {
		return "", fmt.Errorf("service.%s: %s: %w", action, name, err)
	}
	if res != nil && res.ExitCode != 0 {
		return "", fmt.Errorf("service.%s: %s: exit %d: %s", action, name, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return past + " " + name, nil
}

// --- disk.* -----------------------------------------------------------------

func diskUsage(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("disk.usage: no command provider available")
	}
	cmd := "df -P"
	if path := argStr(args, "path", "name", "__id__"); path != "" {
		cmd += " " + shellQuote(path)
	}
	res, err := mctx.Command.Run(ctx, shellCmd(cmd))
	if err != nil {
		return "", fmt.Errorf("disk.usage: %w", err)
	}
	return strings.TrimRight(res.Stdout, "\n"), nil
}

// --- cmd.* ------------------------------------------------------------------

func cmdRun(ctx context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	command := argStr(args, "cmd", "command", "name", "__id__")
	if command == "" {
		return "", fmt.Errorf("cmd.run: command required")
	}
	if mctx == nil || mctx.Command == nil {
		return "", fmt.Errorf("cmd.run: no command provider available")
	}
	opts := shellCmd(command)
	opts.Dir = argStr(args, "cwd", "dir")
	res, err := mctx.Command.Run(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("cmd.run: %s: %w", command, err)
	}
	return strings.TrimRight(res.Stdout, "\n"), nil
}

// --- grains.* ---------------------------------------------------------------

func grainsItem(_ context.Context, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	key := argStr(args, "key", "name", "__id__")
	if key == "" {
		return "", fmt.Errorf("grains.item: grain key required")
	}
	if mctx == nil {
		return "", nil
	}
	val := factLookup(mctx.Facts, key)
	if val == nil {
		return "", nil
	}
	return valueToString(val), nil
}

func grainsItems(_ context.Context, mctx *exec.ModuleContext, _ map[string]any) (string, error) {
	if mctx == nil || mctx.Facts == nil {
		return "", nil
	}
	return valueToString(mctx.Facts), nil
}

// --- helpers ----------------------------------------------------------------

// pkgManager returns the detected package-manager name, or "" if no package
// provider is available on the context.
func pkgManager(mctx *exec.ModuleContext) string {
	if mctx == nil || mctx.Package == nil {
		return ""
	}
	return mctx.Package.Name()
}

// shellCmd builds a CommandOpts that runs cmd through "sh -c".
func shellCmd(cmd string) exec.CommandOpts {
	return exec.CommandOpts{Command: cmd, Shell: true}
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes so
// it is safe to interpolate into a "sh -c" command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// argStr returns the first non-empty argument value among keys, converting
// non-string scalars (numbers, bools) via fmt.Sprintf. Missing keys, nil
// values, and empty strings are skipped.
func argStr(args map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := args[k]
		if !ok {
			continue
		}
		switch s := v.(type) {
		case string:
			if s != "" {
				return s
			}
		case nil:
			// skip
		default:
			return fmt.Sprintf("%v", s)
		}
	}
	return ""
}

// factLookup resolves a dotted key (e.g. "os.family") against a nested facts
// map, returning nil if any segment is missing.
func factLookup(facts map[string]any, key string) any {
	var current any = facts
	for _, part := range strings.Split(key, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[part]
		if !ok {
			return nil
		}
	}
	return current
}

// valueToString renders a fact/arg value as a string: scalars directly,
// composite values (maps/slices) as trimmed YAML.
func valueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", t)
	default:
		data, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return strings.TrimSpace(string(data))
	}
}
