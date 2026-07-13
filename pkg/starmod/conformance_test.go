package starmod_test

import (
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// TestStarlarkDocstringConformance pins the docstring- and position-extraction
// behavior of the pinned, pseudo-versioned go.starlark.net dependency that the
// self-documentation loader relies on. starlark-go extracts a function's Doc
// from the first statement of its body ONLY when that statement is a bare string
// literal (Python semantics), and reports the `def` keyword's source position
// via fn.Position(). If a starlark-go upgrade ever changes either contract, this
// test breaks loudly rather than the loader silently capturing empty docs.
func TestStarlarkDocstringConformance(t *testing.T) {
	const filename = "/pin/mod.star"
	const src = `
def with_doc(id, config):
    """The docstring.

    Second line.
    """
    return {"changed": True}

def no_doc(id, config):
    return {"changed": True}

def first_stmt_not_string(id, config):
    x = "not a docstring"
    return {"changed": True}
`
	thread := &starlark.Thread{Name: filename}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, filename, src, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}

	fnOf := func(name string) *starlark.Function {
		fn, ok := globals[name].(*starlark.Function)
		if !ok {
			t.Fatalf("global %q is not a *starlark.Function (%T)", name, globals[name])
		}
		return fn
	}

	// (1) A leading string literal IS the docstring, preserved verbatim
	// (including internal indentation, which the loader dedents itself).
	withDoc := fnOf("with_doc")
	gotDoc := withDoc.Doc()
	const wantDoc = "The docstring.\n\n    Second line.\n    "
	if gotDoc != wantDoc {
		t.Errorf("with_doc.Doc() = %q, want %q", gotDoc, wantDoc)
	}

	// (2) No leading string literal ⇒ empty docstring.
	if d := fnOf("no_doc").Doc(); d != "" {
		t.Errorf("no_doc.Doc() = %q, want empty", d)
	}

	// (3) A non-string first statement ⇒ empty docstring (only a bare string
	// literal counts).
	if d := fnOf("first_stmt_not_string").Doc(); d != "" {
		t.Errorf("first_stmt_not_string.Doc() = %q, want empty", d)
	}

	// (4) Position reports the source filename and the 1-based line of `def`.
	pos := withDoc.Position()
	if !pos.IsValid() {
		t.Fatal("with_doc.Position() is not valid")
	}
	if pos.Filename() != filename {
		t.Errorf("Position().Filename() = %q, want %q", pos.Filename(), filename)
	}
	if pos.Line != 2 { // `def with_doc` is the second line (leading newline is line 1).
		t.Errorf("Position().Line = %d, want 2", pos.Line)
	}
}
