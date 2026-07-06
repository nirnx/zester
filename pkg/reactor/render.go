package reactor

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/target"
	"github.com/nirnx/zester/pkg/template"
)

// FactsFn serves the emitting peel's facts for the origin_facts render
// variable (masterd wires the retained facts.Index). Implementations must
// never error: unknown peels yield nil/empty maps.
type FactsFn func(peelID string) map[string]any

// funcRe is the post-render allowlist for dispatchable function names.
var funcRe = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// ActionType classifies a normalized action block.
type ActionType string

const (
	// ActionDispatch dispatches a job to targeted peels (dispatch.module,
	// dispatch.state, and the local.* Salt sugar all normalize to it).
	ActionDispatch ActionType = "dispatch"

	// ActionEnroll performs a master-local enrollment transition
	// (enroll.approve / enroll.reject / enroll.revoke).
	ActionEnroll ActionType = "enroll"

	// ActionEventSend emits a derived event for rule chaining.
	ActionEventSend ActionType = "event.send"

	// ActionLog Info-logs a rendered message.
	ActionLog ActionType = "log"
)

// Action is one normalized, post-render-validated reaction block.
type Action struct {
	// BlockID is the YAML block key — part of the deterministic JID.
	BlockID string

	Type ActionType

	// Dispatch fields.
	Function   string
	Target     string
	Args       map[string]any
	StateID    string
	Timeout    time.Duration
	MaxTargets int

	// Enroll fields.
	EnrollOp    string // "approve" | "reject" | "revoke"
	EnrollID    string
	RequirePeel string
	Reason      string

	// Event.send fields.
	Tag  string
	Data map[string]any

	// Log fields.
	Message string
}

// Summary returns a one-line human-readable description (the reactor test
// service's action listing).
func (a Action) Summary() string {
	switch a.Type {
	case ActionDispatch:
		s := fmt.Sprintf("dispatch %s target=%q timeout=%s", a.Function, a.Target, a.Timeout)
		if a.StateID != "" {
			s += fmt.Sprintf(" state_id=%q", a.StateID)
		}
		if len(a.Args) > 0 {
			s += " args=" + formatArgs(a.Args)
		}
		if a.MaxTargets > 0 {
			s += fmt.Sprintf(" max_targets=%d", a.MaxTargets)
		}
		return s
	case ActionEnroll:
		s := fmt.Sprintf("enroll.%s id=%q require_peel=%q", a.EnrollOp, a.EnrollID, a.RequirePeel)
		if a.Reason != "" {
			s += fmt.Sprintf(" reason=%q", a.Reason)
		}
		return s
	case ActionEventSend:
		s := fmt.Sprintf("event.send tag=%q", a.Tag)
		if len(a.Data) > 0 {
			s += " data=" + formatArgs(a.Data)
		}
		return s
	case ActionLog:
		return fmt.Sprintf("log %q", a.Message)
	default:
		return string(a.Type)
	}
}

// formatArgs renders a map deterministically (sorted keys) for summaries.
func formatArgs(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%v", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// NormalizeOptions parameterize action normalization.
type NormalizeOptions struct {
	// EnableChaining permits event.send blocks; when false they are refused
	// at normalize time.
	EnableChaining bool
}

// RenderedRule is the outcome of rendering + normalizing one matched rule.
// A non-empty Errors list means the rule failed post-render validation and
// none of its actions may execute (a partial reaction is worse than none).
type RenderedRule struct {
	Rule    string
	Actions []Action
	Errors  []string
}

// Renderer renders reaction files with the event context and normalizes the
// resulting YAML into typed actions. The template engine runs with
// ModuleFn=nil: any salt[...] use inside a reaction file is a clear render
// error — reactions have no master-side module dispatch surface.
type Renderer struct {
	engine  *template.Engine
	factsFn FactsFn
}

// NewRenderer builds a Renderer. factsFn may be nil (origin_facts renders as
// an empty map).
func NewRenderer(factsFn FactsFn) (*Renderer, error) {
	// Reaction files render from KV snapshots and never resolve filesystem
	// includes, but the engine stats its BasePath at construction — anchor
	// it somewhere guaranteed to exist.
	eng, err := template.NewEngine(template.EngineConfig{BasePath: os.TempDir()})
	if err != nil {
		return nil, fmt.Errorf("reactor: create template engine: %w", err)
	}
	return &Renderer{engine: eng, factsFn: factsFn}, nil
}

// eventContext builds the template variables injected into reaction
// renders. origin is the authoritative subject token (peel ID, _master, or
// _admin); event.peel equals origin only when the origin is a peel.
func (r *Renderer) eventContext(ev event.Event, origin string) map[string]any {
	data := ev.Data
	if data == nil {
		data = map[string]any{}
	}
	peel := ""
	if origin != bus.OriginMaster && origin != bus.OriginAdmin {
		peel = origin
	}
	var originFacts map[string]any
	if peel != "" && r.factsFn != nil {
		originFacts = r.factsFn(peel)
	}
	if originFacts == nil {
		originFacts = map[string]any{}
	}
	return map[string]any{
		"event": map[string]any{
			"id":     ev.ID,
			"tag":    ev.Tag,
			"peel":   peel,
			"origin": origin,
			"depth":  ev.Depth,
			"ts":     ev.TS,
			"data":   data,
		},
		// Salt-compat top-level aliases.
		"tag":          ev.Tag,
		"data":         data,
		"origin_facts": originFacts,
	}
}

// Render renders one reaction file source with the event context and returns
// the rendered YAML text.
func (r *Renderer) Render(ruleRef string, src []byte, ev event.Event, origin string) (string, error) {
	rendered, err := r.engine.RenderString(ruleRef, string(src), template.RenderContext{
		Extra: r.eventContext(ev, origin),
	})
	if err != nil {
		return "", fmt.Errorf("reactor: render %s: %w", ruleRef, err)
	}
	return rendered, nil
}

// RenderRule renders + YAML-parses + normalizes one matched rule. The error
// return covers render and YAML-parse failures (metric result
// "render_error"); per-block normalization/validation failures land in
// RenderedRule.Errors (result "validate_error") and forbid execution.
func (r *Renderer) RenderRule(ruleRef string, src []byte, ev event.Event, origin string, opts NormalizeOptions) (RenderedRule, error) {
	rendered, err := r.Render(ruleRef, src, ev, origin)
	if err != nil {
		return RenderedRule{Rule: ruleRef}, err
	}
	actions, errs, err := NormalizeActions(rendered, opts)
	if err != nil {
		return RenderedRule{Rule: ruleRef}, fmt.Errorf("reactor: parse rendered %s: %w", ruleRef, err)
	}
	return RenderedRule{Rule: ruleRef, Actions: actions, Errors: errs}, nil
}

// NormalizeActions YAML-parses a rendered reaction document and normalizes
// every block into a typed Action, in document order. The error return is a
// document-level YAML failure; block-level normalization/validation problems
// are collected as strings (one per failing block). An empty document yields
// no actions and no errors.
func NormalizeActions(rendered string, opts NormalizeOptions) ([]Action, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(rendered), &root); err != nil {
		return nil, nil, err
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, nil, nil // empty document (e.g. all-conditional template)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("reaction document must be a mapping of blocks, got %s", yamlKind(doc.Kind))
	}

	var actions []Action
	var errs []string
	for i := 0; i+1 < len(doc.Content); i += 2 {
		blockID := doc.Content[i].Value
		act, err := normalizeBlock(blockID, doc.Content[i+1], opts)
		if err != nil {
			errs = append(errs, fmt.Sprintf("block %q: %v", blockID, err))
			continue
		}
		actions = append(actions, act)
	}
	return actions, errs, nil
}

// normalizeBlock turns one "blockID: {action.key: fields}" block into an
// Action.
func normalizeBlock(blockID string, node *yaml.Node, opts NormalizeOptions) (Action, error) {
	if node.Kind != yaml.MappingNode {
		return Action{}, fmt.Errorf("block body must be a mapping with one action key, got %s", yamlKind(node.Kind))
	}
	if len(node.Content) != 2 {
		return Action{}, fmt.Errorf("want exactly one action key per block, got %d", len(node.Content)/2)
	}

	actionKey := node.Content[0].Value
	body := node.Content[1]

	switch {
	case actionKey == "dispatch.module":
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		return buildDispatchModule(blockID, fields)

	case actionKey == "dispatch.state":
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		return buildDispatchState(blockID, fields)

	case strings.HasPrefix(actionKey, "local."):
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		return buildLocalSugar(blockID, strings.TrimPrefix(actionKey, "local."), fields)

	case actionKey == "enroll.approve" || actionKey == "enroll.reject" || actionKey == "enroll.revoke":
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		return buildEnroll(blockID, strings.TrimPrefix(actionKey, "enroll."), fields)

	case actionKey == "event.send":
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		return buildEventSend(blockID, fields, opts)

	case actionKey == "log":
		return buildLog(blockID, body)

	default:
		return Action{}, fmt.Errorf("unknown action %q (allowed: dispatch.module, dispatch.state, local.<mod.func>, enroll.approve/reject/revoke, event.send, log)", actionKey)
	}
}

// flattenFields accepts a mapping or the Salt list-of-single-key-maps form
// and returns a merged field map.
func flattenFields(node *yaml.Node) (map[string]any, error) {
	switch node.Kind {
	case yaml.MappingNode:
		fields := map[string]any{}
		if err := node.Decode(&fields); err != nil {
			return nil, fmt.Errorf("decode fields: %w", err)
		}
		return fields, nil
	case yaml.SequenceNode:
		fields := map[string]any{}
		for _, item := range node.Content {
			one := map[string]any{}
			if err := item.Decode(&one); err != nil {
				return nil, fmt.Errorf("list items must be single-key maps: %w", err)
			}
			for k, v := range one {
				fields[k] = v
			}
		}
		return fields, nil
	default:
		return nil, fmt.Errorf("action fields must be a map or a list of single-key maps, got %s", yamlKind(node.Kind))
	}
}

// checkFieldKeys rejects unknown field keys (typos surface loudly at
// validate time instead of being silently ignored).
func checkFieldKeys(fields map[string]any, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[a] = struct{}{}
	}
	var unknown []string
	for k := range fields {
		if _, ok := set[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown field(s) %s (allowed: %s)", strings.Join(unknown, ", "), strings.Join(allowed, ", "))
	}
	return nil
}

func buildDispatchModule(blockID string, fields map[string]any) (Action, error) {
	if err := checkFieldKeys(fields, "target", "target_type", "function", "args", "state_id", "timeout", "max_targets"); err != nil {
		return Action{}, err
	}
	act := Action{BlockID: blockID, Type: ActionDispatch}

	var err error
	if act.Function, err = stringField(fields, "function", true); err != nil {
		return Action{}, err
	}
	if err := fillDispatchCommon(&act, fields); err != nil {
		return Action{}, err
	}
	return act, validateDispatch(&act)
}

func buildDispatchState(blockID string, fields map[string]any) (Action, error) {
	if err := checkFieldKeys(fields, "target", "target_type", "sls", "highstate", "args", "timeout", "max_targets"); err != nil {
		return Action{}, err
	}
	act := Action{BlockID: blockID, Type: ActionDispatch}

	sls, err := stringField(fields, "sls", false)
	if err != nil {
		return Action{}, err
	}
	highstate := truthy(fields["highstate"])
	switch {
	case sls != "" && highstate:
		return Action{}, fmt.Errorf("dispatch.state: sls and highstate are mutually exclusive")
	case sls != "":
		act.Function = "state.apply"
		act.Args = map[string]any{"mods": sls}
	case highstate:
		act.Function = "state.highstate"
	default:
		return Action{}, fmt.Errorf("dispatch.state: one of sls or highstate is required")
	}

	if extra, ok := fields["args"]; ok {
		extraMap, ok := extra.(map[string]any)
		if !ok {
			return Action{}, fmt.Errorf("args must be a map, got %T", extra)
		}
		if act.Args == nil {
			act.Args = map[string]any{}
		}
		for k, v := range extraMap {
			act.Args[k] = v
		}
	}
	delete(fields, "args") // consumed above; keep fillDispatchCommon from re-reading it
	if err := fillDispatchCommon(&act, fields); err != nil {
		return Action{}, err
	}
	return act, validateDispatch(&act)
}

// buildLocalSugar normalizes the Salt `local.<mod.func>` form
// (tgt/tgt_type/arg/kwarg) into a dispatch action.
func buildLocalSugar(blockID, function string, fields map[string]any) (Action, error) {
	if err := checkFieldKeys(fields, "tgt", "tgt_type", "arg", "kwarg", "timeout"); err != nil {
		return Action{}, err
	}
	act := Action{BlockID: blockID, Type: ActionDispatch, Function: function}

	tgt, err := stringField(fields, "tgt", true)
	if err != nil {
		return Action{}, err
	}
	tgtType, err := stringField(fields, "tgt_type", false)
	if err != nil {
		return Action{}, err
	}
	act.Target, err = canonicalTarget(tgt, tgtType)
	if err != nil {
		return Action{}, err
	}

	switch arg := fields["arg"].(type) {
	case nil:
	case string:
		act.StateID = arg
	case []any:
		if len(arg) > 1 {
			return Action{}, fmt.Errorf("arg: only one positional argument is supported (map extra arguments via kwarg)")
		}
		if len(arg) == 1 {
			act.StateID = fmt.Sprintf("%v", arg[0])
		}
	default:
		return Action{}, fmt.Errorf("arg must be a string or a list, got %T", arg)
	}

	if kw, ok := fields["kwarg"]; ok {
		kwMap, ok := kw.(map[string]any)
		if !ok {
			return Action{}, fmt.Errorf("kwarg must be a map, got %T", kw)
		}
		act.Args = kwMap
	}

	if act.Timeout, err = durationField(fields, "timeout"); err != nil {
		return Action{}, err
	}
	return act, validateDispatch(&act)
}

func buildEnroll(blockID, op string, fields map[string]any) (Action, error) {
	if err := checkFieldKeys(fields, "id", "require_peel", "reason"); err != nil {
		return Action{}, err
	}
	act := Action{BlockID: blockID, Type: ActionEnroll, EnrollOp: op}

	var err error
	if act.EnrollID, err = stringField(fields, "id", true); err != nil {
		return Action{}, err
	}
	// require_peel is MANDATORY at compile time (amendment 10): an enroll
	// action without a peel-ID gate is a rule-compile error, not a default.
	if act.RequirePeel, err = stringField(fields, "require_peel", true); err != nil {
		return Action{}, fmt.Errorf("enroll.%s requires a require_peel glob that the enrollment's peel ID must match: %w", op, err)
	}
	if _, err := CompileMatchGlob(act.RequirePeel); err != nil {
		return Action{}, fmt.Errorf("require_peel: %w", err)
	}
	if act.Reason, err = stringField(fields, "reason", false); err != nil {
		return Action{}, err
	}
	return act, nil
}

func buildEventSend(blockID string, fields map[string]any, opts NormalizeOptions) (Action, error) {
	if !opts.EnableChaining {
		return Action{}, fmt.Errorf("event.send refused: reaction chaining is disabled (reactor.enable_chaining=false)")
	}
	if err := checkFieldKeys(fields, "tag", "data"); err != nil {
		return Action{}, err
	}
	act := Action{BlockID: blockID, Type: ActionEventSend}

	var err error
	if act.Tag, err = stringField(fields, "tag", true); err != nil {
		return Action{}, err
	}
	if err := event.ValidateTag(act.Tag); err != nil {
		return Action{}, err
	}
	if d, ok := fields["data"]; ok {
		dMap, ok := d.(map[string]any)
		if !ok {
			return Action{}, fmt.Errorf("data must be a map, got %T", d)
		}
		act.Data = dMap
	}
	return act, nil
}

func buildLog(blockID string, body *yaml.Node) (Action, error) {
	act := Action{BlockID: blockID, Type: ActionLog}

	if body.Kind == yaml.ScalarNode {
		act.Message = body.Value
	} else {
		fields, err := flattenFields(body)
		if err != nil {
			return Action{}, err
		}
		if err := checkFieldKeys(fields, "message"); err != nil {
			return Action{}, err
		}
		if act.Message, err = stringField(fields, "message", true); err != nil {
			return Action{}, err
		}
	}
	if act.Message == "" {
		return Action{}, fmt.Errorf("log: message is required")
	}
	return act, nil
}

// fillDispatchCommon reads the shared dispatch.* fields (target,
// target_type, args, state_id, timeout, max_targets) into act.
func fillDispatchCommon(act *Action, fields map[string]any) error {
	tgt, err := stringField(fields, "target", true)
	if err != nil {
		return err
	}
	tgtType, err := stringField(fields, "target_type", false)
	if err != nil {
		return err
	}
	if act.Target, err = canonicalTarget(tgt, tgtType); err != nil {
		return err
	}

	if a, ok := fields["args"]; ok {
		aMap, ok := a.(map[string]any)
		if !ok {
			return fmt.Errorf("args must be a map, got %T", a)
		}
		act.Args = aMap
	}
	if act.StateID, err = stringField(fields, "state_id", false); err != nil {
		return err
	}
	if act.Timeout, err = durationField(fields, "timeout"); err != nil {
		return err
	}

	if mt, ok := fields["max_targets"]; ok {
		n, ok := toInt(mt)
		if !ok || n <= 0 {
			return fmt.Errorf("max_targets must be a positive integer, got %v", mt)
		}
		act.MaxTargets = n
	}
	return nil
}

// validateDispatch is the post-render validation gate: the function name
// must match the mod.func allowlist and the target expression must parse
// under pkg/target. Bounds the blast radius of attacker-controlled
// event.data flowing through templates.
func validateDispatch(act *Action) error {
	if !funcRe.MatchString(act.Function) {
		return fmt.Errorf("function %q does not match ^[a-z0-9_]+\\.[a-z0-9_]+$", act.Function)
	}
	if act.Target == "" {
		return fmt.Errorf("target is required")
	}
	tt := target.DetectType(act.Target)
	if _, err := target.NewMatcher(act.Target, tt); err != nil {
		return fmt.Errorf("target %q does not parse as %s: %w", act.Target, tt, err)
	}
	if act.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	return nil
}

// canonicalTarget folds an explicit target type into the expression's
// standard prefix form (pcre -> E@, fact/grain -> G@, settings/pillar -> I@,
// list -> L@) so downstream auto-detection (validation, the resolve seam,
// and the audit TargetExpr) sees one canonical expression.
func canonicalTarget(expr, targetType string) (string, error) {
	if expr == "" {
		return "", fmt.Errorf("target is required")
	}
	var prefix string
	switch strings.ToLower(targetType) {
	case "", "glob", "compound":
		return expr, nil
	case "pcre", "regex":
		prefix = "E@"
	case "fact", "grain", "grains":
		prefix = "G@"
	case "settings", "pillar":
		prefix = "I@"
	case "list":
		prefix = "L@"
	default:
		return "", fmt.Errorf("unknown target type %q (allowed: glob, pcre, fact, settings, list, compound)", targetType)
	}
	if strings.HasPrefix(expr, prefix) {
		return expr, nil
	}
	return prefix + expr, nil
}

// stringField reads a required/optional string field.
func stringField(fields map[string]any, key string, required bool) (string, error) {
	v, ok := fields[key]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, v)
	}
	if required && s == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return s, nil
}

// durationField reads an optional duration field (string duration or number
// of seconds).
func durationField(fields map[string]any, key string) (time.Duration, error) {
	v, ok := fields[key]
	if !ok || v == nil {
		return 0, nil
	}
	d, err := parseFlexDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

// toInt coerces YAML-decoded numbers to int.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
	}
	return 0, false
}

// truthy interprets YAML booleans and common string forms.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(t) {
		case "true", "yes", "1", "on":
			return true
		}
	case int:
		return t != 0
	}
	return false
}

func yamlKind(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}
