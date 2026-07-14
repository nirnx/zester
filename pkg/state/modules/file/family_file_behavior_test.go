package filemod

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// TestFileFamily_MakeDirsContract is the CANONICAL RUNTIME CONTRACT suite for
// the file.* makedirs component (keystone spec §13, maintainer-approved): a
// shared runtime contract, not only a shared declaration. EVERY member
// embedding fileMakeDirsParam runs the same four cases:
//
//	makedirs=false, parents present  -> operation proceeds (no parent error)
//	makedirs=false, parents missing  -> Check reports WOULD-CHANGE with detail
//	                                    naming the parent and the remedy
//	                                    (re-ruled 2026-07-14: dry runs of
//	                                    ordered trees stay valid); Apply FAILS
//	                                    with the contract error, nothing
//	                                    partially created
//	makedirs=true,  parents present  -> operation proceeds
//	makedirs=true,  parents missing  -> parents created (0755), operation
//	                                    proceeds
//
// A member that embeds the component but diverges from this behavior fails
// here — a shared declaration over divergent behavior would make the docs lie
// uniformly, which is worse than honest duplication.
func TestFileFamily_MakeDirsContract(t *testing.T) {
	const parent = "/srv/deep/nest"
	const target = parent + "/thing"

	subjects := []struct {
		module string
		// build constructs the member state for target with the given makedirs
		// flag, pre-seeding fs with whatever else the member needs (sources).
		build func(t *testing.T, fs *exectest.FakeFileExec, makedirs bool) state.State
	}{
		{"file.managed", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			return buildFileFamilyState(t, fs, NewFileManagedBuilder, target,
				map[string]any{"content": "x", "makedirs": md})
		}},
		{"file.copy", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			fs.PreCreate("/srcfile", []byte("payload"), 0o644)
			return buildFileFamilyState(t, fs, NewFileCopyBuilder, target,
				map[string]any{"source": "/srcfile", "makedirs": md})
		}},
		{"file.symlink", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			return buildFileFamilyState(t, fs, NewFileSymlinkBuilder, target,
				map[string]any{"target": "/etc/hosts", "makedirs": md})
		}},
		{"file.touch", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			return buildFileFamilyState(t, fs, NewFileTouchBuilder, target,
				map[string]any{"makedirs": md})
		}},
		{"file.directory", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			return buildFileFamilyState(t, fs, NewFileDirectoryBuilder, target,
				map[string]any{"makedirs": md})
		}},
		{"file.recurse", func(t *testing.T, fs *exectest.FakeFileExec, md bool) state.State {
			fs.PreCreate("/srcdir/f.txt", []byte("payload"), 0o644)
			return buildFileFamilyState(t, fs, NewFileRecurseBuilder, target,
				map[string]any{"source": "/srcdir", "makedirs": md})
		}},
	}

	for _, sub := range subjects {
		t.Run(sub.module, func(t *testing.T) {
			t.Run("false parents-present", func(t *testing.T) {
				fs := exectest.NewFakeFileExec()
				seedParent(t, fs, parent)
				s := sub.build(t, fs, false)
				if _, err := s.Check(context.Background()); err != nil && isParentContractErr(err) {
					t.Fatalf("Check raised the parent error with parents present: %v", err)
				}
				if _, err := s.Apply(context.Background()); err != nil && isParentContractErr(err) {
					t.Fatalf("Apply raised the parent error with parents present: %v", err)
				}
			})

			t.Run("false parents-missing", func(t *testing.T) {
				fs := exectest.NewFakeFileExec()
				s := sub.build(t, fs, false)
				// Re-ruled 2026-07-14 (Salt-aligned): Check reports WOULD-CHANGE
				// with structured detail naming the parent and the remedy —
				// never an error (an earlier state in the run may create it).
				cr, err := s.Check(context.Background())
				if err != nil {
					t.Fatalf("Check errored (must report would-change): %v", err)
				}
				if !cr.NeedsChange {
					t.Fatal("Check did not report a would-change for the missing parent")
				}
				if !strings.Contains(cr.Diff, parent) || !strings.Contains(cr.Diff, "makedirs: true") {
					t.Fatalf("Check detail must NAME the parent and the remedy; got %q", cr.Diff)
				}
				// Apply stays STRICT: the canonical contract error, no partial
				// creation.
				if _, err := s.Apply(context.Background()); !isParentContractErr(err) {
					t.Fatalf("Apply: err=%v, want the canonical parent error", err)
				}
				// Nothing partially created: neither the parent nor the target.
				if _, err := fs.Stat(context.Background(), parent); err == nil {
					t.Fatal("parent was created despite makedirs=false")
				}
				if _, err := fs.Stat(context.Background(), target); err == nil {
					t.Fatal("target was created despite the failed parent check")
				}
			})

			t.Run("true parents-present", func(t *testing.T) {
				fs := exectest.NewFakeFileExec()
				seedParent(t, fs, parent)
				s := sub.build(t, fs, true)
				if _, err := s.Apply(context.Background()); err != nil {
					t.Fatalf("Apply: %v", err)
				}
			})

			t.Run("true parents-missing", func(t *testing.T) {
				fs := exectest.NewFakeFileExec()
				s := sub.build(t, fs, true)
				if _, err := s.Apply(context.Background()); err != nil {
					t.Fatalf("Apply: %v", err)
				}
				info, err := fs.Stat(context.Background(), parent)
				if err != nil {
					t.Fatalf("parent not created with makedirs=true: %v", err)
				}
				// The contract fixes the creation mode at 0755 — assert the
				// EXPLICITLY created entry's perm bits (the fake records the
				// MkdirAll perm; implicit ancestors always stat 0755).
				if perm := info.Mode().Perm(); perm != 0o755 {
					t.Fatalf("parent created with mode %04o, want the contractual 0755", perm)
				}

				// Revert NEVER removes parents makedirs created.
				if rev, ok := s.(state.State); ok {
					if _, err := rev.Revert(context.Background()); err == nil {
						if _, err := fs.Stat(context.Background(), parent); err != nil {
							t.Fatal("Revert removed a parent directory makedirs created")
						}
					}
				}
			})

			t.Run("trailing-slash target is not its own parent", func(t *testing.T) {
				// filepath.Dir("/a/b/") is "/a/b" — without Clean the target
				// would gate on ITSELF (review finding). With real parents
				// present, a trailing-slash target must behave exactly like
				// the clean spelling.
				fs := exectest.NewFakeFileExec()
				seedParent(t, fs, parent)
				s := subFromTarget(t, sub.module, fs, target+"/", false)
				if s == nil {
					return // member's build rejects trailing slash inputs upstream — acceptable
				}
				if _, err := s.Check(context.Background()); isParentContractErr(err) {
					t.Fatalf("trailing-slash target gated on itself: %v", err)
				}
			})
		})
	}
}

// TestFileFamily_MakeDirsOrderedTreeDryRun pins the 2026-07-14 re-ruling: a
// correctly ORDERED tree — A (file.directory) creates the parent, B
// (file.managed, require A) writes a file inside it — must (1) pass a full
// ModeCheck dry run with B reporting would-change (never an error), and
// (2) apply cleanly in ModeApply. The standalone strict-apply case (no A) is
// pinned alongside.
func TestFileFamily_MakeDirsOrderedTreeDryRun(t *testing.T) {
	build := func(fs *exectest.FakeFileExec) []state.State {
		seedParent(t, fs, "/srv")
		mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fs}}
		a, err := NewFileDirectoryBuilder(mctx, modschema.DecodeOptions{})("/srv/app", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := NewFileManagedBuilder(mctx, modschema.DecodeOptions{})("/srv/app/conf", map[string]any{
			"content": "x",
			"require": []any{"file.directory:/srv/app"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return []state.State{a, b}
	}

	// (1) Full-check dry run: SUCCEEDS; B is a would-change naming the parent.
	fs := exectest.NewFakeFileExec()
	states := build(fs)
	runner := state.NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := runner.Run(context.Background(), states, state.ModeCheck)
	if err != nil {
		t.Fatalf("dry run errored: %v", err)
	}
	if !res.Success() {
		t.Fatalf("dry run of a correctly ordered tree FAILED: %+v", res.States)
	}
	var bDiff string
	for name, r := range res.States {
		if strings.Contains(name, "/srv/app/conf") {
			bDiff = r.Diff
		}
	}
	if !strings.Contains(bDiff, "/srv/app") {
		t.Fatalf("B's dry-run detail does not name the pending parent: %q", bDiff)
	}

	// (2) Real apply of the same tree: both states converge.
	fs2 := exectest.NewFakeFileExec()
	states2 := build(fs2)
	res2, err := runner.Run(context.Background(), states2, state.ModeApply)
	if err != nil {
		t.Fatalf("apply run errored: %v", err)
	}
	if !res2.Success() {
		for name, r := range res2.States {
			t.Logf("state %s: err=%v skipped=%v reason=%s", name, r.Error, r.Skipped, r.SkipReason)
		}
		t.Fatal("apply of the ordered tree failed")
	}
	if _, err := fs2.Stat(context.Background(), "/srv/app/conf"); err != nil {
		t.Fatalf("B's file was not written: %v", err)
	}

	// (3) Standalone strict apply: B alone, parent missing, makedirs unset —
	// the canonical contract error at the point the operation actually runs.
	fs3 := exectest.NewFakeFileExec()
	mctx3 := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fs3}}
	b3, err := NewFileManagedBuilder(mctx3, modschema.DecodeOptions{})("/srv/app/conf", map[string]any{"content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b3.Apply(context.Background()); !isParentContractErr(err) {
		t.Fatalf("standalone apply: err=%v, want the canonical parent error", err)
	}
	if _, err := fs3.Stat(context.Background(), "/srv/app"); err == nil {
		t.Fatal("standalone apply partially created the parent")
	}
}

// subFromTarget rebuilds a subject state for an alternate target spelling.
func subFromTarget(t *testing.T, module string, fs *exectest.FakeFileExec, target string, makedirs bool) state.State {
	t.Helper()
	cfg := map[string]any{"makedirs": makedirs}
	var b func(*exec.ModuleContext, modschema.DecodeOptions) state.Builder
	switch module {
	case "file.managed":
		b, cfg["content"] = NewFileManagedBuilder, "x"
	case "file.copy":
		fs.PreCreate("/srcfile", []byte("p"), 0o644)
		b, cfg["source"] = NewFileCopyBuilder, "/srcfile"
	case "file.symlink":
		b, cfg["target"] = NewFileSymlinkBuilder, "/etc/hosts"
	case "file.touch":
		b = NewFileTouchBuilder
	case "file.directory":
		b = NewFileDirectoryBuilder
	case "file.recurse":
		fs.PreCreate("/srcdir/f.txt", []byte("p"), 0o644)
		b, cfg["source"] = NewFileRecurseBuilder, "/srcdir"
	default:
		t.Fatalf("unknown subject %s", module)
	}
	return buildFileFamilyState(t, fs, b, target, cfg)
}

// TestFileFamily_MakeDirsSuiteCompleteness pins that the behavior suite's
// subject list equals the set of modules embedding fileMakeDirsParam — a
// seventh embedder cannot silently skip the contract suite (§13).
func TestFileFamily_MakeDirsSuiteCompleteness(t *testing.T) {
	suite := map[string]bool{
		"file.managed": true, "file.copy": true, "file.symlink": true,
		"file.touch": true, "file.directory": true, "file.recurse": true,
	}
	embedders := map[string]bool{}
	for _, row := range Rows {
		if row.Spec == nil {
			continue
		}
		for _, f := range row.Spec.Info().Params {
			if f.Name == "makedirs" && f.DeclaredBy == "filemod.fileMakeDirsParam" {
				embedders[row.Name] = true
			}
		}
	}
	for m := range embedders {
		if !suite[m] {
			t.Errorf("module %s embeds fileMakeDirsParam but is missing from TestFileFamily_MakeDirsContract's subjects", m)
		}
	}
	for m := range suite {
		if !embedders[m] {
			t.Errorf("suite subject %s does not embed fileMakeDirsParam — remove it or fix the embed", m)
		}
	}
}

// seedParent materializes the parent chain in the fake.
func seedParent(t *testing.T, fs *exectest.FakeFileExec, parent string) {
	t.Helper()
	if err := fs.MkdirAll(context.Background(), parent, 0o755); err != nil {
		t.Fatal(err)
	}
}

// buildFileFamilyState builds a member state with the shared mctx shape.
func buildFileFamilyState(t *testing.T, fs *exectest.FakeFileExec,
	newBuilder func(*exec.ModuleContext, modschema.DecodeOptions) state.Builder,
	id string, cfg map[string]any) state.State {
	t.Helper()
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fs}}
	s, err := newBuilder(mctx, modschema.DecodeOptions{})(id, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return s
}

// isParentContractErr recognizes the canonical contract error shape.
func isParentContractErr(err error) bool {
	return err != nil &&
		strings.Contains(err.Error(), "parent directory") &&
		strings.Contains(err.Error(), "makedirs: true")
}
