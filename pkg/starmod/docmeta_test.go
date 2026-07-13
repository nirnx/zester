package starmod

import (
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// execGlobals executes a Starlark source snippet and returns its globals. The
// filename is stable so fn.Position().Filename() assertions are deterministic.
func execGlobals(t *testing.T, filename, src string) starlark.StringDict {
	t.Helper()
	thread := &starlark.Thread{Name: filename}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, filename, src, nil)
	if err != nil {
		t.Fatalf("exec %s: %v", filename, err)
	}
	return globals
}

func execFunc(t *testing.T, filename, src, fnName string) *starlark.Function {
	t.Helper()
	globals := execGlobals(t, filename, src)
	fn, ok := globals[fnName].(*starlark.Function)
	if !ok {
		t.Fatalf("global %q is not a function (%T)", fnName, globals[fnName])
	}
	return fn
}

func TestParseDocstring_SummaryDescriptionArgs(t *testing.T) {
	doc := "Configure the service.\n\n" +
		"    Ensures the unit file is present and the daemon is running.\n" +
		"    Second description line.\n\n" +
		"    Args:\n" +
		"        path: absolute path to the unit file\n" +
		"        mode: file mode as an octal string\n"

	summary, description, args := parseDocstring(doc)
	if summary != "Configure the service." {
		t.Errorf("summary = %q", summary)
	}
	want := "Ensures the unit file is present and the daemon is running.\nSecond description line."
	if description != want {
		t.Errorf("description = %q, want %q", description, want)
	}
	if args["path"] != "absolute path to the unit file" {
		t.Errorf("args[path] = %q", args["path"])
	}
	if args["mode"] != "file mode as an octal string" {
		t.Errorf("args[mode] = %q", args["mode"])
	}
}

func TestParseDocstring_SingleLine(t *testing.T) {
	summary, description, args := parseDocstring("Just a summary.")
	if summary != "Just a summary." {
		t.Errorf("summary = %q", summary)
	}
	if description != "" {
		t.Errorf("description = %q, want empty", description)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}
}

func TestParseDocstring_Empty(t *testing.T) {
	summary, description, args := parseDocstring("   \n  \n")
	if summary != "" || description != "" || len(args) != 0 {
		t.Errorf("expected all-empty, got summary=%q description=%q args=%v", summary, description, args)
	}
}

func TestParseArgsSection_ContinuationAndSectionStop(t *testing.T) {
	doc := "Summary.\n\n" +
		"    Args:\n" +
		"        url: the download URL\n" +
		"            (must be https)\n" +
		"        dest: the destination directory\n\n" +
		"    Returns:\n" +
		"        A result dict.\n"
	_, _, args := parseDocstring(doc)
	if args["url"] != "the download URL (must be https)" {
		t.Errorf("args[url] = %q (continuation not merged)", args["url"])
	}
	if args["dest"] != "the destination directory" {
		t.Errorf("args[dest] = %q", args["dest"])
	}
	if _, ok := args["Returns"]; ok {
		t.Errorf("Returns section leaked into args: %v", args)
	}
	if len(args) != 2 {
		t.Errorf("args has %d entries, want 2: %v", len(args), args)
	}
}

func TestSplitArgLine_TypeAnnotation(t *testing.T) {
	name, text, ok := splitArgLine("mode (str): the file mode")
	if !ok || name != "mode" || text != "the file mode" {
		t.Errorf("splitArgLine = (%q,%q,%v)", name, text, ok)
	}
	if _, _, ok := splitArgLine("no colon here"); ok {
		t.Error("expected splitArgLine to reject a colon-less line")
	}
}

func TestParseParamDecls_DictForm(t *testing.T) {
	params := map[string]any{
		"path":    map[string]any{"type": "str", "required": true, "usage": "target path"},
		"mode":    map[string]any{"type": "str", "default": "0644", "help": "file mode"},
		"enabled": map[string]any{"type": "bool", "default": true},
	}
	args := map[string]string{"enabled": "whether to enable"}
	decls := parseParamDecls(params, args)

	if len(decls) != 3 {
		t.Fatalf("decls = %d, want 3", len(decls))
	}
	// Sorted order: enabled, mode, path.
	byName := map[string]paramDecl{}
	for _, d := range decls {
		byName[d.name] = d
	}
	if d := byName["path"]; !d.required || d.typ != "str" || d.usage != "target path" {
		t.Errorf("path decl = %+v", d)
	}
	if d := byName["mode"]; !d.hasDefault || d.def != "0644" || d.usage != "file mode" {
		t.Errorf("mode decl = %+v", d)
	}
	// Args docstring supplies usage when the dict does not.
	if d := byName["enabled"]; !d.hasDefault || d.def != "true" || d.usage != "whether to enable" {
		t.Errorf("enabled decl = %+v", d)
	}
}

func TestParseParamDecls_StringShorthand(t *testing.T) {
	decls := parseParamDecls(map[string]any{"path": "the target path"}, nil)
	if len(decls) != 1 || decls[0].usage != "the target path" || decls[0].typ != "" {
		t.Errorf("decls = %+v", decls)
	}
}

func TestScalarLiteral(t *testing.T) {
	cases := []struct {
		in   any
		want string
		ok   bool
	}{
		{"0644", "0644", true},
		{true, "true", true},
		{int64(5), "5", true},
		{uint64(7), "7", true},
		{1.5, "1.5", true},
		{[]any{"a"}, "", false},
		{map[string]any{}, "", false},
		{nil, "", false},
	}
	for _, c := range cases {
		got, ok := scalarLiteral(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("scalarLiteral(%v) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBuildStarSpec_DeclaredParams(t *testing.T) {
	src := `
PARAMS = {
    "path": {"type": "str", "required": True, "usage": "config path"},
    "mode": {"type": "str", "default": "0644"},
}

def configured(id, config):
    """Configure nginx.

    Renders the config and reloads.
    """
    return {"changed": True}
`
	globals := execGlobals(t, "/states/_modules/nginx.star", src)
	fn := globals["configured"].(*starlark.Function)
	moduleParams, _ := collectParamDicts(globals)

	spec, err := buildStarSpec("nginx.configured", fn, moduleParams)
	if err != nil {
		t.Fatal(err)
	}
	if spec.OpenParams {
		t.Error("OpenParams should be false when PARAMS is declared")
	}
	if spec.Module != "nginx.configured" || spec.Kind != "state" {
		t.Errorf("spec module/kind = %q/%q", spec.Module, spec.Kind)
	}
	if spec.NewParams() == nil {
		t.Fatal("NewParams returned nil for a Starlark spec (must be a real synthetic proto)")
	}
	info := spec.Info()
	if info.Doc.Summary != "Configure nginx." {
		t.Errorf("summary = %q", info.Doc.Summary)
	}
	if info.Doc.Description != "Renders the config and reloads." {
		t.Errorf("description = %q", info.Doc.Description)
	}
	// Source note captured from fn.Position().
	foundSource := false
	for _, n := range info.Doc.Notes {
		if n.Title == "Source" && n.Body == "/states/_modules/nginx.star:7" {
			foundSource = true
		}
	}
	if !foundSource {
		t.Errorf("source note missing/incorrect: %+v", info.Doc.Notes)
	}
	// Fields carry declared metadata.
	byName := map[string]bool{}
	for _, f := range info.Params {
		byName[f.Name] = true
		if f.Name == "path" && !f.Required {
			t.Error("path should be required")
		}
		if f.Name == "mode" && f.Default != "0644" {
			t.Errorf("mode default = %q", f.Default)
		}
	}
	if !byName["path"] || !byName["mode"] {
		t.Errorf("missing declared fields: %v", byName)
	}
}

func TestBuildStarSpec_OpenParamsWhenNoDict(t *testing.T) {
	src := `
def deployed(id, config):
    """Deploy the app.

    Args:
        url: the tarball URL
    """
    return {"changed": True}
`
	fn := execFunc(t, "/states/_modules/app.star", src, "deployed")
	spec, err := buildStarSpec("app.deployed", fn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.OpenParams {
		t.Error("OpenParams should be true when no PARAMS/<fn>_params dict is declared")
	}
	// Docstring Args still surface as display-only fields.
	info := spec.Info()
	if len(info.Params) != 1 || info.Params[0].Name != "url" {
		t.Errorf("expected one display-only 'url' field, got %+v", info.Params)
	}
	if info.Params[0].Usage != "the tarball URL" {
		t.Errorf("url usage = %q", info.Params[0].Usage)
	}
}

func TestBuildStarSpec_EmptyParamsIsOpen(t *testing.T) {
	src := `
PARAMS = {}

def go(id, config):
    return {"changed": True}
`
	globals := execGlobals(t, "/states/_modules/x.star", src)
	fn := globals["go"].(*starlark.Function)
	moduleParams, _ := collectParamDicts(globals)
	spec, err := buildStarSpec("x.go", fn, moduleParams)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.OpenParams {
		t.Error("an empty PARAMS dict should leave the module open (no false unknown-key warnings)")
	}
}

func TestCollectParamDicts_PerFunctionAndGlobal(t *testing.T) {
	src := `
PARAMS = {"a": {"type": "str"}}
configured_params = {"b": {"type": "int"}}

def configured(id, config):
    return {"changed": True}
`
	globals := execGlobals(t, "/x.star", src)
	moduleParams, fnParams := collectParamDicts(globals)
	if _, ok := moduleParams["a"]; !ok {
		t.Errorf("module PARAMS not collected: %v", moduleParams)
	}
	if fp, ok := fnParams["configured"]; !ok || fp["b"] == nil {
		t.Errorf("configured_params not collected: %v", fnParams)
	}
}
