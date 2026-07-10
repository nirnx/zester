package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
)

func testPkgrepoMctx(fakeFile *exectest.FakeFileExec, fakeCmd *exectest.FakeCommandExec, family, provider string) *exec.ModuleContext {
	facts := map[string]any{}
	if family != "" {
		facts["os"] = map[string]any{"family": family}
	}
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			File:    fakeFile,
			Command: fakeCmd,
		},
		Facts: facts,
	}
	if provider != "" {
		mctx.Package = exectest.NewFakePackageExec(provider)
	}
	return mctx
}

func TestPkgrepoManagedName(t *testing.T) {
	mctx := testPkgrepoMctx(exectest.NewFakeFileExec(), exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "pkgrepo.managed:docker" {
		t.Errorf("Name: got %q, want %q", s.Name(), "pkgrepo.managed:docker")
	}
}

func TestPkgrepoManagedPrimaryParamDefault(t *testing.T) {
	mctx := testPkgrepoMctx(exectest.NewFakeFileExec(), exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("myrepo", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	r := s.(*PkgrepoManaged)
	if r.RepoName != "myrepo" {
		t.Errorf("RepoName: got %q, want myrepo", r.RepoName)
	}
	if r.HumanName != "myrepo" {
		t.Errorf("HumanName should default to Name, got %q", r.HumanName)
	}
}

func TestPkgrepoManagedRequisites(t *testing.T) {
	mctx := testPkgrepoMctx(exectest.NewFakeFileExec(), exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl":   "deb https://example.com stable main",
		"require":   []any{"pkg.installed:curl"},
		"watch":     []any{"file.managed:/etc/apt/keyrings/docker.gpg"},
		"onchanges": []any{"cmd.run:refresh"},
		"onfail":    []any{"cmd.run:alert"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:curl" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestPkgrepoManagedCheckMissingFile(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when the repo file is missing")
	}
}

func TestPkgrepoManagedApplyAptWritesFile(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"humanname": "Docker CE",
		"baseurl":   "deb https://download.docker.com/linux/ubuntu focal stable",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after apply")
	}
	data, ok := fakeFile.GetFile("/etc/apt/sources.list.d/docker.list")
	if !ok {
		t.Fatal("expected repo file to be written")
	}
	if !strings.Contains(string(data), "deb https://download.docker.com/linux/ubuntu focal stable") {
		t.Errorf("repo file content missing deb line: %q", string(data))
	}
	// apt-get update should have been invoked (Refresh defaults to true).
	found := false
	for _, c := range fakeCmd.Calls() {
		if c.Command == "apt-get" && len(c.Args) > 0 && c.Args[0] == "update" {
			found = true
		}
	}
	if !found {
		t.Error("expected apt-get update to be run")
	}
}

func TestPkgrepoManagedApplyAptIdempotent(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change after the repo file is already written")
	}
}

func TestPkgrepoManagedApplyAptWithKey(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sawCurl, sawAptKey bool
	for _, c := range fakeCmd.Calls() {
		switch c.Command {
		case "curl":
			sawCurl = true
		case "apt-key":
			sawAptKey = true
		}
	}
	if !sawCurl {
		t.Error("expected curl to fetch the signing key")
	}
	if !sawAptKey {
		t.Error("expected apt-key to import the signing key")
	}
}

func TestPkgrepoManagedCheckKeyringMissing(t *testing.T) {
	// The .list content matches, but the declared signing key was never
	// imported: the key is part of the desired state, so Check must report
	// a change (Apply is the only place the key gets imported).
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/apt/sources.list.d/docker.list",
		[]byte("# Managed by Zester: docker\ndeb https://example.com stable main\n"), 0644)
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange when the declared signing key is not imported")
	}
	if !strings.Contains(cr.Diff, "signing key") {
		t.Errorf("diff should name the missing key dimension, got %q", cr.Diff)
	}
}

func TestPkgrepoManagedCheckKeyringPresent(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/apt/sources.list.d/docker.list",
		[]byte("# Managed by Zester: docker\ndeb https://example.com stable main\n"), 0644)
	fakeFile.PreCreate("/etc/apt/keyrings/zester-docker.gpg", []byte("KEYDATA"), 0644)
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when content matches and the keyring exists, diff %q", cr.Diff)
	}
}

func TestPkgrepoManagedCheckKeyringReadErrorFailsPhase(t *testing.T) {
	// Only fs.ErrNotExist means "absent"; any other read failure is an
	// unanswerable probe and must fail Check, not masquerade as a change.
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.PreCreate("/etc/apt/sources.list.d/docker.list",
		[]byte("# Managed by Zester: docker\ndeb https://example.com stable main\n"), 0644)
	fakeFile.SetReadError("/etc/apt/keyrings/zester-docker.gpg", errors.New("permission denied"))
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail on a non-not-exist keyring read error")
	}
}

func TestPkgrepoManagedCheckListReadErrorFailsPhase(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeFile.SetReadError("/etc/apt/sources.list.d/docker.list", errors.New("permission denied"))
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check to fail on a non-not-exist repo-file read error")
	}
}

func TestPkgrepoManagedApplyWritesKeyringAndConverges(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("curl", &exec.CommandResult{Stdout: "KEYDATA", ExitCode: 0}, nil)
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, ok := fakeFile.GetFile("/etc/apt/keyrings/zester-docker.gpg")
	if !ok {
		t.Fatal("expected Apply to persist the fetched key to the keyring path")
	}
	if string(data) != "KEYDATA" {
		t.Errorf("keyring content: got %q, want %q", string(data), "KEYDATA")
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected Apply -> Check convergence with a declared key_url, diff %q", cr.Diff)
	}
}

func TestPkgrepoManagedRevertRemovesKeyring(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetResult("curl", &exec.CommandResult{Stdout: "KEYDATA", ExitCode: 0}, nil)
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{
		"baseurl": "deb https://example.com stable main",
		"key_url": "https://example.com/gpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fakeFile.Exists("/etc/apt/keyrings/zester-docker.gpg") {
		t.Error("expected revert to remove the keyring artifact")
	}
}

func TestPkgrepoManagedRedHatKeyURLNeedsNoKeyring(t *testing.T) {
	// On RedHat the key_url lives in the compared .repo bytes (gpgkey=);
	// dnf fetches it at transaction time — no keyring artifact to probe.
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "redhat", "dnf")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("epel", map[string]any{
		"baseurl": "https://download.example.com/epel/8/x86_64",
		"key_url": "https://download.example.com/RPM-GPG-KEY-EPEL-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected redhat Apply -> Check convergence without a keyring file, diff %q", cr.Diff)
	}
}

func TestPkgrepoManagedApplyYumRepoContent(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "redhat", "dnf")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("epel", map[string]any{
		"humanname": "Extra Packages",
		"baseurl":   "https://download.example.com/epel/8/x86_64",
		"gpgcheck":  true,
		"key_url":   "https://download.example.com/RPM-GPG-KEY-EPEL-8",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, ok := fakeFile.GetFile("/etc/yum.repos.d/epel.repo")
	if !ok {
		t.Fatal("expected .repo file to be written")
	}
	content := string(data)
	for _, want := range []string{
		"[epel]",
		"name=Extra Packages",
		"baseurl=https://download.example.com/epel/8/x86_64",
		"enabled=1",
		"gpgcheck=1",
		"gpgkey=https://download.example.com/RPM-GPG-KEY-EPEL-8",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("repo content missing %q\n---\n%s", want, content)
		}
	}
}

func TestPkgrepoManagedApplyPPA(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("nginx-ppa", map[string]any{"ppa": "ppa:nginx/stable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fakeCmd.Calls() {
		if c.Command == "add-apt-repository" {
			for _, a := range c.Args {
				if a == "ppa:nginx/stable" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected add-apt-repository to be run for the PPA")
	}
}

func TestPkgrepoManagedApplyError(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	fakeCmd.SetError("apt-get", errors.New("update failed"))
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected error when apt-get update fails")
	}
}

func TestPkgrepoManagedRevertRemovesFile(t *testing.T) {
	fakeFile := exectest.NewFakeFileExec()
	fakeCmd := exectest.NewFakeCommandExec()
	mctx := testPkgrepoMctx(fakeFile, fakeCmd, "debian", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := fakeFile.GetFile("/etc/apt/sources.list.d/docker.list"); !ok {
		t.Fatal("precondition: file should exist after apply")
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after revert")
	}
	if fakeFile.Exists("/etc/apt/sources.list.d/docker.list") {
		t.Error("expected repo file to be removed after revert")
	}
}

func TestPkgrepoManagedProviderNameFallback(t *testing.T) {
	// No facts, but the apt package provider is present: family should resolve
	// from the provider name.
	fakeFile := exectest.NewFakeFileExec()
	mctx := testPkgrepoMctx(fakeFile, exectest.NewFakeCommandExec(), "", "apt")
	builder := NewPkgrepoManagedBuilder(mctx)
	s, err := builder("docker", map[string]any{"baseurl": "deb https://example.com stable main"})
	if err != nil {
		t.Fatal(err)
	}
	r := s.(*PkgrepoManaged)
	if r.family != "debian" {
		t.Errorf("family: got %q, want debian (from provider name)", r.family)
	}
}

func TestPkgrepoManagedNoFileProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Command: exectest.NewFakeCommandExec()}}
	builder := NewPkgrepoManagedBuilder(mctx)
	if _, err := builder("docker", map[string]any{}); err == nil {
		t.Error("expected error when no file provider is set")
	}
}

func TestPkgrepoManagedNoCommandProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: exectest.NewFakeFileExec()}}
	builder := NewPkgrepoManagedBuilder(mctx)
	if _, err := builder("docker", map[string]any{}); err == nil {
		t.Error("expected error when no command provider is set")
	}
}

var _ state.State = (*PkgrepoManaged)(nil)
