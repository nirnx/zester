package starmod_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/starmod"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func testMctx() *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: exectest.NewFakePackageExec("test"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
		Logger: slog.Default(),
	}
}

func execStarlark(t *testing.T, mctx *exec.ModuleContext, code string) starlark.StringDict {
	t.Helper()
	builtins := starmod.MakeBuiltins(mctx, map[string]any{"os": "linux"}, map[string]any{"role": "web"})
	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())

	globals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", code, builtins,
	)
	if err != nil {
		t.Fatalf("exec starlark: %v", err)
	}
	return globals
}

func TestBuiltin_CmdRun_HappyPath(t *testing.T) {
	mctx := testMctx()
	fake := mctx.Command.(*exectest.FakeCommandExec)
	fake.SetResult("echo", &exec.CommandResult{
		Stdout:   "hello\n",
		Stderr:   "",
		ExitCode: 0,
	}, nil)

	globals := execStarlark(t, mctx, `
result = cmd_run("echo", args=["hello"])
`)

	d := globals["result"].(*starlark.Dict)
	stdout, _, _ := d.Get(starlark.String("stdout"))
	if string(stdout.(starlark.String)) != "hello\n" {
		t.Errorf("stdout = %v", stdout)
	}
	exitCode, _, _ := d.Get(starlark.String("exit_code"))
	i, _ := exitCode.(starlark.Int).Int64()
	if i != 0 {
		t.Errorf("exit_code = %d", i)
	}
}

func TestBuiltin_CmdRun_NonZeroExit(t *testing.T) {
	mctx := testMctx()
	fake := mctx.Command.(*exectest.FakeCommandExec)
	fake.SetResult("fail", &exec.CommandResult{
		Stdout:   "",
		Stderr:   "error occurred",
		ExitCode: 1,
	}, nil)

	globals := execStarlark(t, mctx, `
result = cmd_run("fail")
`)

	d := globals["result"].(*starlark.Dict)
	exitCode, _, _ := d.Get(starlark.String("exit_code"))
	i, _ := exitCode.(starlark.Int).Int64()
	if i != 1 {
		t.Errorf("exit_code = %d, want 1", i)
	}
}

func TestBuiltin_CmdRun_NilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{Logger: slog.Default()}
	builtins := starmod.MakeBuiltins(mctx, nil, nil)
	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())

	_, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", `cmd_run("echo")`, builtins,
	)
	if err == nil {
		t.Error("expected error for nil command provider")
	}
}

func TestBuiltin_FileReadWrite(t *testing.T) {
	mctx := testMctx()

	execStarlark(t, mctx, `
file_write("/tmp/test.txt", "hello world", mode=0o644)
content = file_read("/tmp/test.txt")
`)

	fake := mctx.File.(*exectest.FakeFileExec)
	data, ok := fake.GetFile("/tmp/test.txt")
	if !ok {
		t.Fatal("file not created")
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q", string(data))
	}
}

func TestBuiltin_FileAppend(t *testing.T) {
	mctx := testMctx()
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/tmp/append.txt", []byte("line1\n"), 0o644)

	execStarlark(t, mctx, `
file_append("/tmp/append.txt", "line2\n")
`)

	data, _ := fake.GetFile("/tmp/append.txt")
	if string(data) != "line1\nline2\n" {
		t.Errorf("content = %q", string(data))
	}
}

func TestBuiltin_FileExists(t *testing.T) {
	mctx := testMctx()
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/tmp/exists.txt", []byte("data"), 0o644)

	globals := execStarlark(t, mctx, `
yes = file_exists("/tmp/exists.txt")
no = file_exists("/tmp/nope.txt")
`)

	if globals["yes"] != starlark.True {
		t.Error("expected True for existing file")
	}
	if globals["no"] != starlark.False {
		t.Error("expected False for non-existing file")
	}
}

func TestBuiltin_FileStat(t *testing.T) {
	mctx := testMctx()
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/tmp/stat.txt", []byte("data"), 0o644)

	globals := execStarlark(t, mctx, `
info = file_stat("/tmp/stat.txt")
missing = file_stat("/tmp/nope.txt")
`)

	d := globals["info"].(*starlark.Dict)
	sizeVal, _, _ := d.Get(starlark.String("size"))
	size, _ := sizeVal.(starlark.Int).Int64()
	if size != 4 {
		t.Errorf("size = %d, want 4", size)
	}

	if globals["missing"] != starlark.None {
		t.Error("expected None for missing file")
	}
}

func TestBuiltin_FileRemove(t *testing.T) {
	mctx := testMctx()
	fake := mctx.File.(*exectest.FakeFileExec)
	fake.PreCreate("/tmp/remove.txt", []byte("data"), 0o644)

	execStarlark(t, mctx, `
file_remove("/tmp/remove.txt")
`)

	if fake.Exists("/tmp/remove.txt") {
		t.Error("file should be removed")
	}
}

func TestBuiltin_FileMkdir(t *testing.T) {
	mctx := testMctx()

	execStarlark(t, mctx, `
file_mkdir("/tmp/newdir", mode=0o755)
`)

	fake := mctx.File.(*exectest.FakeFileExec)
	if !fake.Exists("/tmp/newdir") {
		t.Error("directory should be created")
	}
}

func TestBuiltin_PkgIsInstalled(t *testing.T) {
	mctx := testMctx()
	fake := mctx.Package.(*exectest.FakePackageExec)
	fake.PreInstall("nginx", "1.0")

	globals := execStarlark(t, mctx, `
yes = pkg_is_installed("nginx")
no = pkg_is_installed("apache2")
`)

	if globals["yes"] != starlark.True {
		t.Error("nginx should be installed")
	}
	if globals["no"] != starlark.False {
		t.Error("apache2 should not be installed")
	}
}

func TestBuiltin_PkgInstall(t *testing.T) {
	mctx := testMctx()

	execStarlark(t, mctx, `
pkg_install("vim")
`)

	fake := mctx.Package.(*exectest.FakePackageExec)
	if !fake.IsInstalledSync("vim") {
		t.Error("vim should be installed")
	}
}

func TestBuiltin_PkgRemove(t *testing.T) {
	mctx := testMctx()
	fake := mctx.Package.(*exectest.FakePackageExec)
	fake.PreInstall("vim", "")

	execStarlark(t, mctx, `
pkg_remove("vim")
`)

	if fake.IsInstalledSync("vim") {
		t.Error("vim should be removed")
	}
}

func TestBuiltin_PkgNilProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
		Logger: slog.Default(),
	}
	builtins := starmod.MakeBuiltins(mctx, nil, nil)
	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())

	_, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", `pkg_is_installed("nginx")`, builtins,
	)
	if err == nil {
		t.Error("expected error for nil package provider")
	}
}

func TestBuiltin_HTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "ok")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	mctx := testMctx()
	globals := execStarlark(t, mctx, fmt.Sprintf(`
result = http_get("%s")
`, srv.URL))

	d := globals["result"].(*starlark.Dict)
	statusVal, _, _ := d.Get(starlark.String("status"))
	status, _ := statusVal.(starlark.Int).Int64()
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}

	bodyVal, _, _ := d.Get(starlark.String("body"))
	if string(bodyVal.(starlark.String)) != `{"status":"ok"}` {
		t.Errorf("body = %v", bodyVal)
	}
}

func TestBuiltin_HTTPGet_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "not found")
	}))
	defer srv.Close()

	mctx := testMctx()
	globals := execStarlark(t, mctx, fmt.Sprintf(`
result = http_get("%s")
`, srv.URL))

	d := globals["result"].(*starlark.Dict)
	statusVal, _, _ := d.Get(starlark.String("status"))
	status, _ := statusVal.(starlark.Int).Int64()
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestBuiltin_HTTPPost(t *testing.T) {
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		receivedBody = string(body)
		w.WriteHeader(201)
		fmt.Fprint(w, "created")
	}))
	defer srv.Close()

	mctx := testMctx()
	globals := execStarlark(t, mctx, fmt.Sprintf(`
result = http_post("%s", body='{"key":"value"}')
`, srv.URL))

	d := globals["result"].(*starlark.Dict)
	statusVal, _, _ := d.Get(starlark.String("status"))
	status, _ := statusVal.(starlark.Int).Int64()
	if status != 201 {
		t.Errorf("status = %d, want 201", status)
	}
	if receivedBody != `{"key":"value"}` {
		t.Errorf("received body = %q", receivedBody)
	}
}

func TestBuiltin_HTTPRequest_PUT(t *testing.T) {
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(200)
	}))
	defer srv.Close()

	mctx := testMctx()
	execStarlark(t, mctx, fmt.Sprintf(`
result = http_request("PUT", "%s", body="data")
`, srv.URL))

	if receivedMethod != "PUT" {
		t.Errorf("method = %q, want PUT", receivedMethod)
	}
}

func TestBuiltin_JSON_RoundTrip(t *testing.T) {
	mctx := testMctx()
	globals := execStarlark(t, mctx, `
encoded = json.encode({"key": "value", "num": 42})
decoded = json.decode(encoded)
`)

	decoded := globals["decoded"]
	d, ok := decoded.(*starlark.Dict)
	if !ok {
		t.Fatalf("expected dict, got %T", decoded)
	}
	keyVal, _, _ := d.Get(starlark.String("key"))
	if string(keyVal.(starlark.String)) != "value" {
		t.Errorf("key = %v", keyVal)
	}
}

func TestBuiltin_Base64_RoundTrip(t *testing.T) {
	mctx := testMctx()
	globals := execStarlark(t, mctx, `
encoded = base64_encode("hello world")
decoded = base64_decode(encoded)
`)

	encoded := string(globals["encoded"].(starlark.String))
	expected := base64.StdEncoding.EncodeToString([]byte("hello world"))
	if encoded != expected {
		t.Errorf("encoded = %q, want %q", encoded, expected)
	}

	decoded := string(globals["decoded"].(starlark.String))
	if decoded != "hello world" {
		t.Errorf("decoded = %q, want %q", decoded, "hello world")
	}
}

func TestBuiltin_HashSHA256(t *testing.T) {
	mctx := testMctx()
	globals := execStarlark(t, mctx, `
h = hash_sha256("hello")
`)

	got := string(globals["h"].(starlark.String))
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("hello")))
	if got != expected {
		t.Errorf("hash = %q, want %q", got, expected)
	}
}

func TestBuiltin_Sleep_ContextCancel(t *testing.T) {
	mctx := testMctx()
	builtins := starmod.MakeBuiltins(mctx, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", ctx)

	_, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", `sleep(10)`, builtins,
	)
	if err == nil {
		t.Error("expected error from canceled context")
	}
}

func TestBuiltin_FactsAndSettings(t *testing.T) {
	mctx := testMctx()
	facts := map[string]any{"os": "linux", "arch": "amd64"}
	settings := map[string]any{"role": "web"}
	builtins := starmod.MakeBuiltins(mctx, facts, settings)

	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())

	globals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", `
os_val = facts["os"]
role_val = settings["role"]
`, builtins,
	)
	if err != nil {
		t.Fatal(err)
	}

	if string(globals["os_val"].(starlark.String)) != "linux" {
		t.Errorf("facts[os] = %v", globals["os_val"])
	}
	if string(globals["role_val"].(starlark.String)) != "web" {
		t.Errorf("settings[role] = %v", globals["role_val"])
	}
}

func TestBuiltin_FactsFrozen(t *testing.T) {
	mctx := testMctx()
	builtins := starmod.MakeBuiltins(mctx, map[string]any{"os": "linux"}, nil)

	thread := &starlark.Thread{Name: "test"}
	thread.SetLocal("context", context.Background())

	_, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread, "test.star", `facts["new_key"] = "value"`, builtins,
	)
	if err == nil {
		t.Error("expected error when mutating frozen facts dict")
	}
}
