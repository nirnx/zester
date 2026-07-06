package starmod

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/exec"
	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// MakeBuiltins creates the predeclared Starlark environment for custom modules.
// Three categories:
// 1. Exec-layer builtins (cmd_run, file_*, pkg_*) delegate to ModuleContext providers.
// 2. Direct Go stdlib builtins (http_*, base64_*, hash_*, sleep).
// 3. Starlark standard modules (json) from go.starlark.net/lib.
func MakeBuiltins(mctx *exec.ModuleContext, facts, settings map[string]any) starlark.StringDict {
	d := starlark.StringDict{}

	// -- Command execution --
	d["cmd_run"] = starlark.NewBuiltin("cmd_run", makeCmdRun(mctx))

	// -- File operations --
	d["file_read"] = starlark.NewBuiltin("file_read", makeFileRead(mctx))
	d["file_write"] = starlark.NewBuiltin("file_write", makeFileWrite(mctx))
	d["file_append"] = starlark.NewBuiltin("file_append", makeFileAppend(mctx))
	d["file_exists"] = starlark.NewBuiltin("file_exists", makeFileExists(mctx))
	d["file_stat"] = starlark.NewBuiltin("file_stat", makeFileStat(mctx))
	d["file_remove"] = starlark.NewBuiltin("file_remove", makeFileRemove(mctx))
	d["file_mkdir"] = starlark.NewBuiltin("file_mkdir", makeFileMkdir(mctx))

	// -- Package management --
	d["pkg_is_installed"] = starlark.NewBuiltin("pkg_is_installed", makePkgIsInstalled(mctx))
	d["pkg_install"] = starlark.NewBuiltin("pkg_install", makePkgInstall(mctx))
	d["pkg_remove"] = starlark.NewBuiltin("pkg_remove", makePkgRemove(mctx))

	// -- HTTP --
	d["http_get"] = starlark.NewBuiltin("http_get", makeHTTPGet())
	d["http_post"] = starlark.NewBuiltin("http_post", makeHTTPPost())
	d["http_request"] = starlark.NewBuiltin("http_request", makeHTTPRequest())

	// -- JSON (from go.starlark.net/lib/json) --
	d["json"] = json.Module

	// -- Utilities --
	d["base64_encode"] = starlark.NewBuiltin("base64_encode", builtinBase64Encode)
	d["base64_decode"] = starlark.NewBuiltin("base64_decode", builtinBase64Decode)
	d["hash_sha256"] = starlark.NewBuiltin("hash_sha256", builtinHashSHA256)
	d["sleep"] = starlark.NewBuiltin("sleep", builtinSleep)

	// -- Logging --
	d["log"] = makeLogModule(mctx.Logger)

	// -- Facts and Settings (frozen, read-only dicts) --
	if facts != nil {
		fv, err := GoToStarlark(facts)
		if err == nil {
			fv.(*starlark.Dict).Freeze()
			d["facts"] = fv
		}
	}
	if d["facts"] == nil {
		empty := starlark.NewDict(0)
		empty.Freeze()
		d["facts"] = empty
	}

	if settings != nil {
		sv, err := GoToStarlark(settings)
		if err == nil {
			sv.(*starlark.Dict).Freeze()
			d["settings"] = sv
		}
	}
	if d["settings"] == nil {
		empty := starlark.NewDict(0)
		empty.Freeze()
		d["settings"] = empty
	}

	return d
}

// threadCtx extracts the context stored in the Starlark thread.
func threadCtx(thread *starlark.Thread) context.Context {
	if ctx, ok := thread.Local("context").(context.Context); ok {
		return ctx
	}
	return context.Background()
}

// ---------- Command execution ----------

func makeCmdRun(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("cmd_run: command provider not available")
		}

		var command string
		var starArgs *starlark.List
		var cwd string
		var env *starlark.Dict
		var shell bool

		if err := starlark.UnpackArgs("cmd_run", args, kwargs,
			"command", &command,
			"args?", &starArgs,
			"cwd?", &cwd,
			"env?", &env,
			"shell?", &shell,
		); err != nil {
			return nil, err
		}

		opts := exec.CommandOpts{
			Command: command,
			Shell:   shell,
			Dir:     cwd,
		}

		if starArgs != nil {
			for i := 0; i < starArgs.Len(); i++ {
				if s, ok := starArgs.Index(i).(starlark.String); ok {
					opts.Args = append(opts.Args, string(s))
				}
			}
		}

		if env != nil {
			opts.Env = make(map[string]string, env.Len())
			for _, item := range env.Items() {
				k, _ := item[0].(starlark.String)
				v, _ := item[1].(starlark.String)
				opts.Env[string(k)] = string(v)
			}
		}

		ctx := threadCtx(thread)
		result, err := mctx.Command.Run(ctx, opts)

		// cmd_run never raises on non-zero exit — returns the result dict.
		d := starlark.NewDict(3)
		if result != nil {
			d.SetKey(starlark.String("stdout"), starlark.String(result.Stdout))
			d.SetKey(starlark.String("stderr"), starlark.String(result.Stderr))
			d.SetKey(starlark.String("exit_code"), starlark.MakeInt(result.ExitCode))
		} else {
			d.SetKey(starlark.String("stdout"), starlark.String(""))
			d.SetKey(starlark.String("stderr"), starlark.String(""))
			d.SetKey(starlark.String("exit_code"), starlark.MakeInt(-1))
		}

		if err != nil {
			d.SetKey(starlark.String("error"), starlark.String(err.Error()))
		}

		return d, nil
	}
}

// ---------- File operations ----------

func makeFileRead(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_read: file provider not available")
		}
		var path string
		if err := starlark.UnpackPositionalArgs("file_read", args, kwargs, 1, &path); err != nil {
			return nil, err
		}
		data, err := mctx.File.ReadFile(threadCtx(thread), path)
		if err != nil {
			return nil, fmt.Errorf("file_read: %w", err)
		}
		return starlark.String(string(data)), nil
	}
}

func makeFileWrite(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_write: file provider not available")
		}
		var path, content string
		var mode int = 0o644
		if err := starlark.UnpackArgs("file_write", args, kwargs,
			"path", &path,
			"content", &content,
			"mode?", &mode,
		); err != nil {
			return nil, err
		}
		if err := mctx.File.WriteFile(threadCtx(thread), path, []byte(content), fs.FileMode(mode)); err != nil {
			return nil, fmt.Errorf("file_write: %w", err)
		}
		return starlark.None, nil
	}
}

func makeFileAppend(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_append: file provider not available")
		}
		var path, content string
		if err := starlark.UnpackPositionalArgs("file_append", args, kwargs, 2, &path, &content); err != nil {
			return nil, err
		}
		ctx := threadCtx(thread)
		existing, err := mctx.File.ReadFile(ctx, path)
		if err != nil {
			existing = nil // file doesn't exist yet
		}
		combined := append(existing, []byte(content)...)
		if err := mctx.File.WriteFile(ctx, path, combined, 0o644); err != nil {
			return nil, fmt.Errorf("file_append: %w", err)
		}
		return starlark.None, nil
	}
}

func makeFileExists(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_exists: file provider not available")
		}
		var path string
		if err := starlark.UnpackPositionalArgs("file_exists", args, kwargs, 1, &path); err != nil {
			return nil, err
		}
		_, err := mctx.File.Stat(threadCtx(thread), path)
		return starlark.Bool(err == nil), nil
	}
}

func makeFileStat(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_stat: file provider not available")
		}
		var path string
		if err := starlark.UnpackPositionalArgs("file_stat", args, kwargs, 1, &path); err != nil {
			return nil, err
		}
		info, err := mctx.File.Stat(threadCtx(thread), path)
		if err != nil {
			return starlark.None, nil
		}
		d := starlark.NewDict(3)
		d.SetKey(starlark.String("size"), starlark.MakeInt64(info.Size()))
		d.SetKey(starlark.String("mode"), starlark.MakeInt(int(info.Mode().Perm())))
		d.SetKey(starlark.String("is_dir"), starlark.Bool(info.IsDir()))
		return d, nil
	}
}

func makeFileRemove(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_remove: file provider not available")
		}
		var path string
		if err := starlark.UnpackPositionalArgs("file_remove", args, kwargs, 1, &path); err != nil {
			return nil, err
		}
		if err := mctx.File.Remove(threadCtx(thread), path); err != nil {
			return nil, fmt.Errorf("file_remove: %w", err)
		}
		return starlark.None, nil
	}
}

func makeFileMkdir(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file_mkdir: file provider not available")
		}
		var path string
		var mode int = 0o755
		if err := starlark.UnpackArgs("file_mkdir", args, kwargs,
			"path", &path,
			"mode?", &mode,
		); err != nil {
			return nil, err
		}
		if err := mctx.File.MkdirAll(threadCtx(thread), path, fs.FileMode(mode)); err != nil {
			return nil, fmt.Errorf("file_mkdir: %w", err)
		}
		return starlark.None, nil
	}
}

// ---------- Package management ----------

func makePkgIsInstalled(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg_is_installed: package provider not available")
		}
		var name string
		if err := starlark.UnpackPositionalArgs("pkg_is_installed", args, kwargs, 1, &name); err != nil {
			return nil, err
		}
		installed, err := mctx.Package.IsInstalled(threadCtx(thread), name)
		if err != nil {
			return nil, fmt.Errorf("pkg_is_installed: %w", err)
		}
		return starlark.Bool(installed), nil
	}
}

func makePkgInstall(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg_install: package provider not available")
		}
		var name, version string
		if err := starlark.UnpackArgs("pkg_install", args, kwargs,
			"name", &name,
			"version?", &version,
		); err != nil {
			return nil, err
		}
		if err := mctx.Package.Install(threadCtx(thread), name, version); err != nil {
			return nil, fmt.Errorf("pkg_install: %w", err)
		}
		return starlark.None, nil
	}
}

func makePkgRemove(mctx *exec.ModuleContext) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if mctx.Package == nil {
			return nil, fmt.Errorf("pkg_remove: package provider not available")
		}
		var name string
		if err := starlark.UnpackPositionalArgs("pkg_remove", args, kwargs, 1, &name); err != nil {
			return nil, err
		}
		if err := mctx.Package.Remove(threadCtx(thread), name); err != nil {
			return nil, fmt.Errorf("pkg_remove: %w", err)
		}
		return starlark.None, nil
	}
}

// ---------- HTTP ----------

func makeHTTPGet() func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var url string
		var headers *starlark.Dict
		var timeout int = 30

		if err := starlark.UnpackArgs("http_get", args, kwargs,
			"url", &url,
			"headers?", &headers,
			"timeout?", &timeout,
		); err != nil {
			return nil, err
		}

		return doHTTP(threadCtx(thread), "GET", url, "", headers, timeout)
	}
}

func makeHTTPPost() func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var url, body string
		var headers *starlark.Dict
		var timeout int = 30

		if err := starlark.UnpackArgs("http_post", args, kwargs,
			"url", &url,
			"body?", &body,
			"headers?", &headers,
			"timeout?", &timeout,
		); err != nil {
			return nil, err
		}

		return doHTTP(threadCtx(thread), "POST", url, body, headers, timeout)
	}
}

func makeHTTPRequest() func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var method, url, body string
		var headers *starlark.Dict
		var timeout int = 30

		if err := starlark.UnpackArgs("http_request", args, kwargs,
			"method", &method,
			"url", &url,
			"body?", &body,
			"headers?", &headers,
			"timeout?", &timeout,
		); err != nil {
			return nil, err
		}

		return doHTTP(threadCtx(thread), method, url, body, headers, timeout)
	}
}

func doHTTP(ctx context.Context, method, url, body string, headers *starlark.Dict, timeout int) (starlark.Value, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", err)
	}

	if headers != nil {
		for _, item := range headers.Items() {
			k, _ := item[0].(starlark.String)
			v, _ := item[1].(starlark.String)
			req.Header.Set(string(k), string(v))
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http_request: read body: %w", err)
	}

	// Convert response headers to a Starlark dict.
	respHeaders := starlark.NewDict(len(resp.Header))
	for k, vals := range resp.Header {
		if len(vals) > 0 {
			respHeaders.SetKey(starlark.String(k), starlark.String(vals[0]))
		}
	}

	d := starlark.NewDict(3)
	d.SetKey(starlark.String("status"), starlark.MakeInt(resp.StatusCode))
	d.SetKey(starlark.String("body"), starlark.String(string(respBody)))
	d.SetKey(starlark.String("headers"), respHeaders)
	return d, nil
}

// ---------- Utilities ----------

func builtinBase64Encode(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackPositionalArgs("base64_encode", args, kwargs, 1, &s); err != nil {
		return nil, err
	}
	return starlark.String(base64.StdEncoding.EncodeToString([]byte(s))), nil
}

func builtinBase64Decode(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackPositionalArgs("base64_decode", args, kwargs, 1, &s); err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64_decode: %w", err)
	}
	return starlark.String(string(decoded)), nil
}

func builtinHashSHA256(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	if err := starlark.UnpackPositionalArgs("hash_sha256", args, kwargs, 1, &s); err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(s))
	return starlark.String(fmt.Sprintf("%x", h)), nil
}

func builtinSleep(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seconds float64
	if err := starlark.UnpackPositionalArgs("sleep", args, kwargs, 1, &seconds); err != nil {
		return nil, err
	}
	ctx := threadCtx(thread)
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return starlark.None, nil
}

// ---------- Logging ----------

func makeLogModule(logger *slog.Logger) *starlarkstruct.Module {
	if logger == nil {
		logger = slog.Default()
	}
	return &starlarkstruct.Module{
		Name: "log",
		Members: starlark.StringDict{
			"info": starlark.NewBuiltin("log.info", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				if args.Len() < 1 {
					return nil, fmt.Errorf("log.info: requires at least 1 argument")
				}
				logger.Info(args[0].String())
				return starlark.None, nil
			}),
			"warn": starlark.NewBuiltin("log.warn", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				if args.Len() < 1 {
					return nil, fmt.Errorf("log.warn: requires at least 1 argument")
				}
				logger.Warn(args[0].String())
				return starlark.None, nil
			}),
			"error": starlark.NewBuiltin("log.error", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
				if args.Len() < 1 {
					return nil, fmt.Errorf("log.error: requires at least 1 argument")
				}
				logger.Error(args[0].String())
				return starlark.None, nil
			}),
		},
	}
}
