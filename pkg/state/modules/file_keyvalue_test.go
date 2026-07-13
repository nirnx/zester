package modules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/modschema/schematest"
	"github.com/nirnx/zester/pkg/state"
)

func testFileKVMctx() *exec.ModuleContext {
	return &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: &exec.OSFileExec{}}}
}

func TestFileKeyValueName(t *testing.T) {
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})("/etc/os-release", map[string]any{
		"key": "NAME", "value": "Zester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "file.keyvalue:/etc/os-release" {
		t.Errorf("Name: got %q", s.Name())
	}
}

func TestFileKeyValueNoEntries(t *testing.T) {
	_, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})("x", map[string]any{})
	if err == nil {
		t.Fatal("expected error when no entries provided")
	}
}

func TestFileKeyValueUpdateExisting(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "sysctl.conf", "net.ipv4.ip_forward=0\nother=1\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "net.ipv4.ip_forward", "value": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange")
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "net.ipv4.ip_forward=1\nother=1\n" {
		t.Errorf("content: got %q", string(got))
	}

	cr2, _ := s.Check(ctx)
	if cr2.NeedsChange {
		t.Error("expected idempotent second check")
	}
}

func TestFileKeyValueAppendNew(t *testing.T) {
	ctx := context.Background()
	path := writeTempFile(t, "env", "EXISTING=1\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"entries": map[string]any{"NEWKEY": "val"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "EXISTING=1\nNEWKEY=val\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileKeyValueCustomSeparatorTolerant(t *testing.T) {
	ctx := context.Background()
	// Existing line uses spaced separator; matcher tolerates surrounding spaces.
	path := writeTempFile(t, "sysctl", "kernel.pid_max = 4096\n")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "kernel.pid_max", "value": "4096", "separator": " = ",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change for already-correct spaced kv, diff=%q", cr.Diff)
	}
}

func TestFileKeyValueCreatesMissingFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"entries": map[string]any{"A": "1", "B": "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "A=1\nB=2\n" {
		t.Errorf("content: got %q", string(got))
	}
}

func TestFileKeyValueRevert(t *testing.T) {
	ctx := context.Background()
	original := "K=old\n"
	path := writeTempFile(t, "kv", original)
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "K", "value": "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Errorf("revert content: got %q", string(got))
	}
}

func TestFileKeyValueFreshInstanceRevertIsNoOp(t *testing.T) {
	ctx := context.Background()
	original := "K=old\n"
	path := writeTempFile(t, "sysctl.conf", original)
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "K", "value": "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if ar.Changed {
		t.Error("fresh-instance revert must not report a change")
	}
	if ar.Diff != fsxNothingToRevert {
		t.Errorf("Diff: got %q, want %q", ar.Diff, fsxNothingToRevert)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file must survive a fresh-instance revert: %v", err)
	}
	if string(got) != original {
		t.Errorf("content after revert: got %q, want %q", string(got), original)
	}
}

func TestFileKeyValueRevertRemovesCreatedFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "K", "value": "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed reverting a file created by this instance's Apply")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected created file removed on same-instance revert")
	}
}

func TestFileKeyValueRevertCreatedToleratesMissing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "new.conf")
	s, err := NewFileKeyValueBuilder(testFileKVMctx(), modschema.DecodeOptions{})(path, map[string]any{
		"key": "K", "value": "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	// The created file vanished externally; revert must tolerate it —
	// file-absent already IS the reverted state (same semantics as file.copy).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revert(ctx); err != nil {
		t.Fatalf("Revert must tolerate an already-missing created file: %v", err)
	}
}

func TestFileKeyValueReadErrorFailsCheckAndApply(t *testing.T) {
	ctx := context.Background()
	original := "keep=me\nK=old\n"
	fake := exectest.NewFakeFileExec()
	fake.PreCreate("/etc/sysctl.conf", []byte(original), 0644)
	fake.SetReadError("/etc/sysctl.conf", errors.New("input/output error"))
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{File: fake}}
	s, err := NewFileKeyValueBuilder(mctx, modschema.DecodeOptions{})("/etc/sysctl.conf", map[string]any{
		"key": "K", "value": "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx); err == nil {
		t.Error("Check: want error when read fails with a non-not-exist error")
	}
	if _, err := s.Apply(ctx); err == nil {
		t.Error("Apply: want error when read fails with a non-not-exist error")
	}
	// A read error must never truncate the file down to just the managed entries.
	if got, _ := fake.GetFile("/etc/sysctl.conf"); string(got) != original {
		t.Errorf("file must not be modified on a read error: got %q", string(got))
	}
}

var _ state.State = (*FileKeyValue)(nil)

// TestFileKeyValueContract replays the permanent differential contract fixtures
// against the migrated fileKeyValueSpec decoder. The decode wrapper reproduces
// the builder's module-local merge — key_values ∪ entries (entries winning a
// per-key collision), then the single key/value injection — so the union,
// entries-wins-collision, key-requires-value, and no-entries cases are pinned,
// mirroring service.dead's DisableOnApply and user.present's resolveGroupFacets
// projections. It pins the key_values/entries/Entries StringMap decode (via the
// schematest StringMap matcher), and the flagged BD-5 (a composite key_values
// value is rejected) and BD-6 (wrong-typed name) divergences.
func TestFileKeyValueContract(t *testing.T) {
	decode := func(id string, config map[string]any) (any, error) {
		var f FileKeyValue
		if _, err := fileKeyValueSpec.Decode(id, config, &f, modschema.DecodeOptions{}); err != nil {
			return nil, err
		}
		f.Entries = paramtypes.StringMap{}
		for k, v := range f.KeyValues {
			f.Entries[k] = v
		}
		for k, v := range f.EntriesInput {
			f.Entries[k] = v
		}
		if f.Key != "" {
			v, present := config["value"]
			if !present || v == nil {
				return nil, fmt.Errorf("file.keyvalue: key %q requires a value", f.Key)
			}
			f.Entries[f.Key] = f.Value
		}
		if len(f.Entries) == 0 {
			return nil, fmt.Errorf("file.keyvalue: no entries")
		}
		return &f, nil
	}
	schematest.RunContract(t, decode, "testdata/contract/file.keyvalue.yaml")
}
