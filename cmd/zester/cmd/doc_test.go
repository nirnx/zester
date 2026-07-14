package cmd

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/moduledoc"
)

// runDocForTest invokes runDoc with a synthetic command carrying the --json
// flag and a captured stdout, bypassing the rootCmd machinery. Color is
// explicitly OFF: without the pin, colorEnabled would fall through to
// shouldColor()'s real-stdout TTY check and the byte-identity assertions below
// would fail when the test binary runs attached to a terminal.
func runDocForTest(t *testing.T, jsonFlag bool, args ...string) (string, error) {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().Bool("json", false, "")
	c.Flags().Bool("no-color", true, "")
	c.Flags().Bool("force-color", false, "")
	if jsonFlag {
		if err := c.Flags().Set("json", "true"); err != nil {
			t.Fatalf("set json flag: %v", err)
		}
	}
	var buf bytes.Buffer
	c.SetOut(&buf)
	err := runDoc(c, args)
	return buf.String(), err
}

// zester doc <module> must render byte-identically to modschema.RenderText over
// the same embedded ModuleInfo — the same currency the peel-side sys.doc uses.
func TestDoc_RendersKnownModule(t *testing.T) {
	mi, ok := moduledoc.Lookup("pkg.removed")
	if !ok {
		t.Fatal("Lookup(pkg.removed) failed")
	}
	want := modschema.RenderText(mi) + "\n"

	out, err := runDocForTest(t, false, "pkg.removed")
	if err != nil {
		t.Fatalf("runDoc: %v", err)
	}
	if out != want {
		t.Errorf("rendered output differs from modschema.RenderText\n got: %q\nwant: %q", out, want)
	}
}

// Bare `zester doc` lists documented modules grouped by family, families sorted.
func TestDoc_ListGroupsByFamily(t *testing.T) {
	out, err := runDocForTest(t, false)
	if err != nil {
		t.Fatalf("runDoc: %v", err)
	}
	if !strings.HasPrefix(out, "Documented modules:") {
		t.Errorf("missing header, got: %q", out[:min(40, len(out))])
	}
	for _, want := range []string{"file.managed", "pkg.installed", "service.running", "user.present"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing module %q", want)
		}
	}
	// Family headers present and sorted: file < pkg < service < user.
	iFile := strings.Index(out, "\nfile\n")
	iPkg := strings.Index(out, "\npkg\n")
	iService := strings.Index(out, "\nservice\n")
	iUser := strings.Index(out, "\nuser\n")
	if iFile < 0 || iPkg < 0 || iService < 0 || iUser < 0 {
		t.Fatalf("missing a family header: file=%d pkg=%d service=%d user=%d", iFile, iPkg, iService, iUser)
	}
	if iFile >= iPkg || iPkg >= iService || iService >= iUser {
		t.Errorf("families not sorted: file=%d pkg=%d service=%d user=%d", iFile, iPkg, iService, iUser)
	}
	if !strings.Contains(out, "Run 'zester doc <module>'") {
		t.Error("missing trailing usage hint")
	}
}

// An unknown module close to a real one yields a nearest-match suggestion.
func TestDoc_UnknownModuleSuggests(t *testing.T) {
	_, err := runDocForTest(t, false, "pkg.removd") // one deletion from pkg.removed
	if err == nil {
		t.Fatal("expected error for unknown module")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown module") {
		t.Errorf("error should say 'unknown module': %s", msg)
	}
	if !strings.Contains(msg, "pkg.removed") {
		t.Errorf("error should suggest pkg.removed: %s", msg)
	}
	if !strings.Contains(msg, "did you mean") {
		t.Errorf("error should offer a suggestion: %s", msg)
	}
}

// A far-off unknown module errors without a spurious suggestion.
func TestDoc_UnknownModuleNoSuggestion(t *testing.T) {
	_, err := runDocForTest(t, false, "zzz.nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown module")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("did not expect a suggestion: %s", err.Error())
	}
}

func TestDoc_JSONSingleModule(t *testing.T) {
	out, err := runDocForTest(t, true, "pkg.removed")
	if err != nil {
		t.Fatalf("runDoc --json: %v", err)
	}
	var mi modschema.ModuleInfo
	if err := json.Unmarshal([]byte(out), &mi); err != nil {
		t.Fatalf("output is not valid ModuleInfo JSON: %v\n%s", err, out)
	}
	if mi.Module != "pkg.removed" {
		t.Errorf("Module = %q, want pkg.removed", mi.Module)
	}
	if !mi.HasSpec {
		t.Error("HasSpec = false")
	}
}

func TestDoc_JSONAll(t *testing.T) {
	out, err := runDocForTest(t, true)
	if err != nil {
		t.Fatalf("runDoc --json: %v", err)
	}
	var all []modschema.ModuleInfo
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatalf("output is not a valid ModuleInfo array: %v", err)
	}
	if len(all) != len(moduledoc.All()) {
		t.Errorf("got %d modules, want %d", len(all), len(moduledoc.All()))
	}
}

func TestModuleFamily(t *testing.T) {
	tests := map[string]string{
		"file.managed":  "file",
		"pkg.installed": "pkg",
		"user.present":  "user",
		"nodots":        "nodots",
		"a.b.c":         "a",
	}
	for in, want := range tests {
		if got := moduleFamily(in); got != want {
			t.Errorf("moduleFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	tests := map[string]string{
		"single":          "single",
		"  trimmed  ":     "trimmed",
		"first\nsecond":   "first",
		"\n\nlead\ntrail": "lead",
	}
	for in, want := range tests {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "", 3},
		{"pkg.removd", "pkg.removed", 1},
		{"kitten", "sitting", 3},
	}
	for _, tt := range tests {
		if got := editDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("editDistance(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSuggestModules(t *testing.T) {
	got := suggestModules("pkg.removd")
	if len(got) == 0 || got[0] != "pkg.removed" {
		t.Errorf("suggestModules(pkg.removd) = %v, want first pkg.removed", got)
	}
	if s := suggestModules("zzz.nonexistent"); len(s) != 0 {
		t.Errorf("suggestModules(zzz.nonexistent) = %v, want none", s)
	}
	if s := suggestModules("pkg.instaled"); len(s) == 0 || s[0] != "pkg.installed" {
		t.Errorf("suggestModules(pkg.instaled) = %v, want first pkg.installed", s)
	}
}

func TestModuleNameCandidates(t *testing.T) {
	// Candidates = every documented module PLUS each family name (the family
	// form 'zester doc ssh_auth' is a valid query), deduplicated.
	all := moduleNameCandidates("")
	fams := map[string]bool{}
	for _, mi := range moduledoc.All() {
		if fam := moduleFamily(mi.Module); fam != mi.Module {
			fams[fam] = true
		}
	}
	if want := len(moduledoc.All()) + len(fams); len(all) != want {
		t.Errorf("empty prefix returned %d, want %d (modules + families)", len(all), want)
	}
	if !contains(all, "ssh_auth") {
		t.Errorf("candidates missing the ssh_auth family name")
	}
	pkgs := moduleNameCandidates("pkg.")
	if len(pkgs) == 0 {
		t.Fatal("pkg. prefix returned no candidates")
	}
	for _, c := range pkgs {
		if !strings.HasPrefix(c, "pkg.") {
			t.Errorf("candidate %q does not have prefix pkg.", c)
		}
	}
	if !contains(pkgs, "pkg.installed") {
		t.Errorf("pkg. candidates missing pkg.installed: %v", pkgs)
	}
	if f := moduleNameCandidates("file"); !contains(f, "file.managed") {
		t.Errorf("file candidates missing file.managed: %v", f)
	}
}

func TestCompleteExecModule(t *testing.T) {
	// First positional (the target): no module completion.
	if got, dir := completeExecModule(nil, nil, ""); got != nil || dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("first positional: got %v dir %v", got, dir)
	}
	// Second positional (the module): completes module names.
	got, dir := completeExecModule(nil, []string{"*"}, "pkg.")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v", dir)
	}
	if !contains(got, "pkg.installed") {
		t.Errorf("second positional completion missing pkg.installed: %v", got)
	}
	// Families are DOC-surface queries, not callable exec surfaces: the exec
	// positional must never complete a bare family name (PR-19 review — a
	// completed `zester 'web' ssh_auth` always fails on the peel).
	if famGot, _ := completeExecModule(nil, []string{"*"}, "ssh_"); contains(famGot, "ssh_auth") {
		t.Errorf("exec completion offered the bare family name ssh_auth: %v", famGot)
	} else if !contains(famGot, "ssh_auth.present") {
		t.Errorf("exec completion missing ssh_auth.present: %v", famGot)
	}
	// Third+ positional (module args): no module completion.
	if got, _ := completeExecModule(nil, []string{"*", "pkg.installed"}, ""); got != nil {
		t.Errorf("third positional: got %v, want nil", got)
	}
}

func TestCompleteModuleNames(t *testing.T) {
	got, dir := completeModuleNames(nil, nil, "file")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v", dir)
	}
	if !contains(got, "file.managed") {
		t.Errorf("completion missing file.managed: %v", got)
	}
	// Once the single module arg is present, no further completion.
	if got, _ := completeModuleNames(nil, []string{"file.managed"}, ""); got != nil {
		t.Errorf("with arg present: got %v, want nil", got)
	}
}

func contains(s []string, want string) bool {
	return slices.Contains(s, want)
}

// TestRunDoc_FamilyForm pins the Salt-parity family form of the OFFLINE doc
// command: `zester doc ssh_auth` renders every documented ssh_auth.* module
// (through the same RenderTextAll the live sys.doc family form uses) instead
// of erroring with "unknown module".
func TestRunDoc_FamilyForm(t *testing.T) {
	out, err := runDocForTest(t, false, "ssh_auth")
	if err != nil {
		t.Fatalf("family form: %v", err)
	}
	iAbs := strings.Index(out, "ssh_auth.absent (state)")
	iPre := strings.Index(out, "ssh_auth.present (state)")
	if iAbs < 0 || iPre < 0 || iAbs > iPre {
		t.Fatalf("family doc missing/misordered members: abs=%d pre=%d", iAbs, iPre)
	}
	if strings.Contains(out, "user.present (state)") {
		t.Error("family doc leaked a non-family module")
	}
}

// TestRunDoc_FamilyJSON pins the --json family form: an ARRAY of ModuleInfo.
func TestRunDoc_FamilyJSON(t *testing.T) {
	out, err := runDocForTest(t, true, "ssh_auth")
	if err != nil {
		t.Fatalf("family --json: %v", err)
	}
	var infos []map[string]any
	if err := json.Unmarshal([]byte(out), &infos); err != nil {
		t.Fatalf("family --json is not a JSON array: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("family --json: %d entries, want 2", len(infos))
	}
}

// TestRunDoc_FamilyUnknownStillErrors keeps the miss behavior: a name that is
// neither a module nor a family errors.
func TestRunDoc_FamilyUnknownStillErrors(t *testing.T) {
	if _, err := runDocForTest(t, false, "totally-bogus"); err == nil {
		t.Fatal("unknown non-family name did not error")
	}
}

// --- Color (--force-color / --no-color) ---

var testANSIRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func testStripANSI(s string) string { return testANSIRe.ReplaceAllString(s, "") }

// runDocForTestColor invokes runDoc with the color flags registered (the real
// command inherits them as persistent root flags) and set as requested.
func runDocForTestColor(t *testing.T, flags map[string]string, args ...string) string {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().Bool("json", false, "")
	c.Flags().Bool("no-color", false, "")
	c.Flags().Bool("force-color", false, "")
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set %s flag: %v", k, err)
		}
	}
	var buf bytes.Buffer
	c.SetOut(&buf)
	if err := runDoc(c, args); err != nil {
		t.Fatalf("runDoc: %v", err)
	}
	return buf.String()
}

// --force-color colorizes the render even without a TTY, and stripping the
// codes reproduces the plain render exactly.
func TestDoc_ForceColorRendersANSI(t *testing.T) {
	// The plain baseline pins color OFF explicitly — deriving it from the
	// non-TTY test stdout would break under a TTY-attached test run.
	plain := runDocForTestColor(t, map[string]string{"no-color": "true"}, "file.managed")
	colored := runDocForTestColor(t, map[string]string{"force-color": "true"}, "file.managed")

	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("--force-color output has no ANSI codes:\n%s", colored)
	}
	if !strings.Contains(colored, "\x1b[1;36mfile.managed\x1b[0m") {
		t.Errorf("--force-color output missing colored module header:\n%.200s", colored)
	}
	if got := testStripANSI(colored); got != plain {
		t.Errorf("stripping ANSI does not reproduce the plain render\n got: %q\nwant: %q", got, plain)
	}
}

// --no-color always wins, even against an explicit --force-color.
func TestDoc_NoColorWinsOverForceColor(t *testing.T) {
	out := runDocForTestColor(t, map[string]string{"force-color": "true", "no-color": "true"}, "file.managed")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("--no-color output contains ANSI codes:\n%q", out)
	}
}

// The bare index is colorized too (heading, family names, module names) and
// stripping reproduces the plain index.
func TestDoc_ForceColorIndex(t *testing.T) {
	plain := runDocForTestColor(t, map[string]string{"no-color": "true"})
	colored := runDocForTestColor(t, map[string]string{"force-color": "true"})

	for _, want := range []string{
		colorBoldYellow + "Documented modules:" + colorReset,
		colorBoldCyan + "file" + colorReset,
		colorGreen + "file.managed" + colorReset,
	} {
		if !strings.Contains(colored, want) {
			t.Errorf("colored index missing %q:\n%.400s", want, colored)
		}
	}
	if got := testStripANSI(colored); got != plain {
		t.Errorf("stripping ANSI does not reproduce the plain index\n got: %q\nwant: %q", got, plain)
	}
}

// The lossless invariant over EVERY embedded module doc — a colorizer/grammar
// drift on any real module (not just synthetic fixtures) fails here.
func TestDoc_ColorizeLosslessOverAllEmbedded(t *testing.T) {
	all := moduledoc.All()
	if len(all) == 0 {
		t.Fatal("no embedded module docs")
	}
	for _, mi := range all {
		plain := modschema.RenderText(mi)
		if got := testStripANSI(modschema.ColorizeDoc(plain)); got != plain {
			t.Errorf("%s: stripANSI(ColorizeDoc()) != RenderText()\n got: %q\nwant: %q", mi.Module, got, plain)
		}
	}
	family := modschema.RenderTextAll(all)
	if got := testStripANSI(modschema.ColorizeDoc(family)); got != family {
		t.Error("stripANSI(ColorizeDoc()) over the full RenderTextAll differs from the plain render")
	}
}
