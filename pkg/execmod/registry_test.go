package execmod_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/execmod"
)

func TestRegistryRegisterHasNames(t *testing.T) {
	r := execmod.NewRegistry()

	if r.Has("foo.bar") {
		t.Fatal("empty registry should not have foo.bar")
	}
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("empty registry Names: got %v", names)
	}

	r.Register("b.two", func(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
		return "two", nil
	})
	r.Register("a.one", func(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
		return "one", nil
	})

	if !r.Has("a.one") || !r.Has("b.two") {
		t.Fatal("registered functions should be present")
	}

	// Names must be sorted.
	got := r.Names()
	want := []string{"a.one", "b.two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names: got %v want %v", got, want)
	}
}

func TestRegistryRegisterNilIgnored(t *testing.T) {
	r := execmod.NewRegistry()
	r.Register("nil.fn", nil)
	if r.Has("nil.fn") {
		t.Fatal("nil function must not be registered")
	}
}

func TestRegistryRegisterOverride(t *testing.T) {
	r := execmod.NewRegistry()
	r.Register("x.y", func(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
		return "first", nil
	})
	r.Register("x.y", func(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
		return "second", nil
	})
	out, err := r.Call(context.Background(), "x.y", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "second" {
		t.Fatalf("override: got %q want second", out)
	}
}

func TestRegistryCallUnknown(t *testing.T) {
	r := execmod.NewRegistry()
	_, err := r.Call(context.Background(), "does.not_exist", nil, nil)
	if !errors.Is(err, execmod.ErrUnknownFunction) {
		t.Fatalf("Call unknown: got %v, want ErrUnknownFunction", err)
	}
}

func TestRegistryCallNilArgsNormalised(t *testing.T) {
	r := execmod.NewRegistry()
	r.Register("check.nil", func(_ context.Context, _ *exec.ModuleContext, args map[string]any) (string, error) {
		if args == nil {
			t.Error("args should be normalised to non-nil map")
		}
		return "", nil
	})
	if _, err := r.Call(context.Background(), "check.nil", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRegistryHasStarterSet(t *testing.T) {
	r := execmod.DefaultRegistry()
	want := []string{
		"test.echo", "test.version", "test.true", "test.false",
		"pkg.version", "pkg.list_pkgs",
		"service.status", "service.start", "service.stop", "service.restart",
		"disk.usage", "cmd.run", "sys.list_functions",
		"grains.item", "grains.items",
	}
	for _, name := range want {
		if !r.Has(name) {
			t.Errorf("DefaultRegistry missing %q", name)
		}
	}
}
