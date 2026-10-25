// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Configurable hooks (README § Manage, Configurable hooks): an admin's declarative rules around
// what a run does, beside the Go hooks (view only, hooks.go). A rule (specs/hooks/<name>.json) names
// an event (before a tool or subagent call, after it, a finding admitted, the task's finish), what it
// matches (a tool or subagent by name, or a prefix ending in *, and an optional condition on a field)
// and one bounded action: block (with a message), approve (a lawyer's gate, the existing mechanism),
// check (a named subagent's typed verdict blocks or annotates), webhook (an SSRF-checked POST, fire and
// forget unless it waits) or limitation (a line in the deliverable's Limitations). Rules run in
// order (then by name); a blocking result stops the rest. A rule that fails (a check unfunded or
// malformed, a waited webhook that failed, a field it cannot read) fails closed (blocks) or open (lets
// it through), as it says. Model calls are metered from the caller's budget and recorded with the
// call's step; every outcome is a ledger row (kind hookrun) written with the step, so a resumed run
// neither repeats nor loses one, and a log line.

const hooksDir = engineDir + "specs/hooks/"

// HookRule is a configurable hook's file.
type HookRule struct {
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Order   int        `json:"order"`
	Event   string     `json:"event"` // before_call, after_call, finding_admitted, task_finish
	Match   string     `json:"match"` // a tool or subagent name, a prefix ending in *, or * (before/after_call only)
	When    *Cond      `json:"when"`  // null: always
	Action  HookAction `json:"action"`
	Failure string     `json:"failure"` // closed or open
}

// Cond is a condition on one field: of the call's input (before_call; after_call also output.*),
// of the finding (finding_admitted: claim, issueId, verified, low, documents), or of the finish
// (task_finish: stop, the stop reason).
type Cond struct {
	Field string `json:"field"`
	Op    string `json:"op"` // eq, ne, contains, exists, absent, gt, lt
	Value any    `json:"value"`
}

// HookAction is what a rule does.
type HookAction struct {
	Type    string   `json:"type"` // block, approve, check, webhook, limitation
	Message string   `json:"message"`
	Agent   string   `json:"agent,omitempty"`
	Webhook *Webhook `json:"webhook,omitempty"`
}

// Webhook is a POST of the event (JSON) to an https URL with a secret header; Wait makes it
// blocking: the event goes on only when it answers 2xx (30 s at most), otherwise it never waits
// (10 s, its answer only logged).
type Webhook struct {
	URL  string `json:"url"`
	Wait bool   `json:"wait"`
	Auth
}

var (
	hookEvents  = []string{"before_call", "after_call", "finding_admitted", "task_finish"}
	hookActions = map[string][]string{"before_call": {"block", "approve", "check", "webhook", "limitation"}, "after_call": {"block", "check", "webhook", "limitation"},
		"finding_admitted": {"block", "approve", "check", "webhook", "limitation"}, "task_finish": {"block", "check", "webhook", "limitation"}}
	condOps   = []string{"eq", "ne", "contains", "exists", "absent", "gt", "lt"}
	fieldPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
)

// checkIn is what a check subagent is sent; its answer must hold verdict and reason.
var checkIn = map[string]any{"event": "before_call", "subject": "retrieve", "text": "{}"}

// kHookRun is the ledger's record of each hook outcome (key: rule, subject and a sequence).
const kHookRun = "hookrun"

// HookOutcome is one rule's outcome on one event.
type HookOutcome struct {
	Rule    string `json:"rule"`
	Event   string `json:"event"`
	Subject string `json:"subject"` // the tool or subagent, the finding id, or "deliverable"
	Outcome string `json:"outcome"` // allowed, blocked, gated, approved, annotated, noted, sent, failed-open, failed-closed
	Note    string `json:"note,omitempty"`
	At      string `json:"at"`
}

// rules are the set's enabled rules of event, in order.
func (s *Specs) rules(event string) []HookRule {
	var out []HookRule
	for _, n := range slices.Sorted(maps.Keys(s.hookRules)) {
		if r := s.hookRules[n]; r.Enabled && r.Event == event {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b HookRule) int { return a.Order - b.Order })
	return out
}

// summary is one line on what the rule does, for the catalogue.
func (r HookRule) summary() string {
	what := map[string]string{"block": "blocks", "approve": "asks a lawyer to approve", "check": "has " + r.Action.Agent + " check", "webhook": "posts to a webhook on",
		"limitation": "adds a Limitations line on"}[r.Action.Type]
	on := map[string]string{"before_call": "each call of " + cmpStr(r.Match == "", "*", r.Match) + " before it runs", "after_call": "each answer of " + cmpStr(r.Match == "", "*", r.Match),
		"finding_admitted": "each admitted finding", "task_finish": "the deliverable before it is rendered"}[r.Event]
	cond := ""
	if r.When != nil {
		cond = fmt.Sprintf(" when %s %s %v", r.When.Field, r.When.Op, r.When.Value)
	}
	return fmt.Sprintf("Order %d: %s %s%s; fails %s.", r.Order, what, on, cond, r.Failure)
}

func (r HookRule) matches(name string) bool {
	switch m := r.Match; {
	case m == "*" || m == "":
		return true
	case strings.HasSuffix(m, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(m, "*"))
	default:
		return m == name
	}
}

// holds is whether c holds of doc (a JSON object); an error when the field cannot be compared.
func (c *Cond) holds(doc map[string]any) (bool, error) {
	if c == nil {
		return true, nil
	}
	var v any = doc
	found := true
	for _, k := range strings.Split(c.Field, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			found = false
			break
		}
		if v, ok = m[k]; !ok || v == nil {
			found = false
			break
		}
	}
	switch c.Op {
	case "exists":
		return found, nil
	case "absent":
		return !found, nil
	}
	if !found {
		return c.Op == "ne", nil
	}
	switch c.Op {
	case "eq", "ne":
		eq := fmt.Sprint(v) == fmt.Sprint(c.Value)
		return eq == (c.Op == "eq"), nil
	case "contains":
		switch x := v.(type) {
		case string:
			return strings.Contains(strings.ToLower(x), strings.ToLower(fmt.Sprint(c.Value))), nil
		case []any:
			return slices.ContainsFunc(x, func(y any) bool { return fmt.Sprint(y) == fmt.Sprint(c.Value) }), nil
		}
		return false, fmt.Errorf("%s is neither text nor a list", c.Field)
	}
	a, aok := number(v)
	b, bok := number(c.Value)
	if !aok || !bok {
		return false, fmt.Errorf("%s or its value is not a number", c.Field)
	}
	return map[string]bool{"gt": a > b, "lt": a < b}[c.Op], nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// hookRunner runs a run's rules: the runtime's Hooks for calls, and the finding and finish events.
type hookRunner struct {
	e   *Engine
	r   *run
	rt  *agent.Runtime
	sp  *Specs
	seq *atomic.Int64
}

type inHookKey struct{}

// event is one occurrence a rule may act on.
type event struct {
	name, subject string
	doc           map[string]any // what conditions read
	text          string         // what a check reads and a webhook is sent (bounded)
	docIDs        []string
}

// verdict is what a run of the rules decided: blocked (with why), gated (a gate opened or still
// open), notes for Limitations, and the model usage the checks spent.
type hookVerdict struct {
	blocked, gated string
	notes          []string
	usage          agent.Usage
}

// run applies the event's rules in order, spending checks from b.
func (h hookRunner) run(ctx context.Context, ev event, rules []HookRule, b *agent.Budget, taskID string) (hookVerdict, error) {
	var v hookVerdict
	if ctx.Value(inHookKey{}) != nil || len(rules) == 0 { // a check's own calls run no hooks
		return v, nil
	}
	ctx = context.WithValue(ctx, inHookKey{}, true)
	for _, rule := range rules {
		if !rule.matches(ev.subject) {
			continue
		}
		ok, cerr := rule.When.holds(ev.doc)
		if cerr == nil && !ok {
			continue
		}
		outcome, note, err := "allowed", "", cerr
		if err == nil {
			outcome, note, err = h.act(ctx, rule, ev, b, taskID, &v)
		}
		if err != nil {
			blocking := rule.Action.Type != "limitation" && !(rule.Action.Type == "webhook" && (rule.Action.Webhook == nil || !rule.Action.Webhook.Wait))
			if rule.Failure == "closed" && blocking {
				outcome, note = "failed-closed", err.Error()
				v.blocked = fmt.Sprintf("hook %s could not run and fails closed (%s)", rule.Name, strings.TrimPrefix(err.Error(), "remote: "))
			} else {
				outcome, note = "failed-open", err.Error()
			}
		}
		if werr := h.record(ctx, taskID, HookOutcome{Rule: rule.Name, Event: ev.name, Subject: ev.subject, Outcome: outcome, Note: truncate(note, 500)}); werr != nil {
			return v, werr
		}
		if v.blocked != "" || v.gated != "" {
			break
		}
	}
	return v, nil
}

func (h hookRunner) record(ctx context.Context, taskID string, o HookOutcome) error {
	o.At = time.Now().UTC().Format(time.RFC3339)
	h.e.log.Info("hook", "task", taskID, "rule", o.Rule, "event", o.Event, "subject", o.Subject, "outcome", o.Outcome, "note", o.Note)
	// keyed by the lead step it ran in and its order there: a step re-run after a crash (its writes
	// never committed) writes the same keys, a resumed run's later steps new ones
	return h.e.write(ctx, taskID, kHookRun, fmt.Sprintf("%06d|%06d|%s|%s", h.r.step.Load(), h.seq.Add(1), o.Rule, o.Subject), o, false)
}

// act does one rule's action; err is the rule's failure (its policy decides).
func (h hookRunner) act(ctx context.Context, rule HookRule, ev event, b *agent.Budget, taskID string, v *hookVerdict) (string, string, error) {
	msg := cmpStr(rule.Action.Message != "", rule.Action.Message, "hook "+rule.Name)
	switch rule.Action.Type {
	case "block":
		v.blocked = "blocked by hook " + rule.Name + ": " + msg
		return "blocked", msg, nil
	case "limitation":
		v.notes = append(v.notes, msg)
		return "noted", msg, h.e.write(ctx, taskID, kNote, "hook:"+rule.Name, msg, true)
	case "approve":
		return h.approve(ctx, rule, ev, taskID, v, msg)
	case "webhook":
		return h.webhook(ctx, rule, ev, taskID)
	}
	// check: the named subagent reads the event and answers {verdict, reason}
	in, _ := json.Marshal(map[string]any{"event": ev.name, "subject": ev.subject, "text": ev.text})
	res, err := h.rt.Invoke(ctx, agent.Agent{Name: "hook_" + rule.Name, Visible: []string{rule.Action.Agent}}, agent.Call{TaskID: taskID, DocumentIDs: ev.docIDs},
		rule.Action.Agent, in, b, false, 0)
	v.usage.Add(res.Usage)
	if err != nil {
		var f *agent.Failure
		var ref *agent.Refusal
		if errors.As(err, &f) || errors.As(err, &ref) || errors.Is(err, agent.ErrToolOutput) || errors.Is(err, agent.ErrTurnCap) || modelErr(err) {
			return "", "", fmt.Errorf("check %s: %s", rule.Action.Agent, strings.TrimPrefix(err.Error(), "engine: "))
		}
		return "", "", err
	}
	var out struct{ Verdict, Reason string }
	_ = json.Unmarshal(res.Output, &out)
	switch out.Verdict {
	case "allow":
		return "allowed", out.Reason, nil
	case "block":
		v.blocked = "blocked by hook " + rule.Name + " (" + rule.Action.Agent + "): " + truncate(out.Reason, 500)
		return "blocked", out.Reason, nil
	case "annotate":
		note := msg + ": " + truncate(out.Reason, 500)
		v.notes = append(v.notes, note)
		return "annotated", out.Reason, h.e.write(ctx, taskID, kNote, "hook:"+rule.Name+":"+ev.subject, note, true)
	}
	return "", "", fmt.Errorf("check %s answered verdict %q (allow, block or annotate)", rule.Action.Agent, truncate(out.Verdict, 40))
}

// modelErr: a check's model failed (unconfigured, rate limited, …): the rule's failure, not the run's.
func modelErr(err error) bool {
	code, _, _ := ErrorType(err)
	return code != 500 && code != 499
}

// approve opens a gate for a lawyer (kind hook; a finding's is its own finding gate, see
// findingHooks), or lets the call through once the gate for the same call is approved.
func (h hookRunner) approve(ctx context.Context, rule HookRule, ev event, taskID string, v *hookVerdict, msg string) (string, string, error) {
	if ev.name == "finding_admitted" { // the finding's own gate (G7) is the lawyer's decision: no second gate
		v.gated = msg
		return "gated", "its finding gate opens when a section cites it", nil
	}
	sum := sha256.Sum256([]byte(ev.subject + "\x00" + ev.text))
	id := "H-" + rule.Name + "-" + hex.EncodeToString(sum[:4])
	gs, err := load[Gate](ctx, h.e, taskID, kGate)
	if err != nil {
		return "", "", err
	}
	if i := slices.IndexFunc(gs, func(g Gate) bool { return g.GateID == id }); i >= 0 {
		switch gs[i].Decision {
		case "approved":
			return "approved", id, nil
		case "rejected":
			v.blocked = "a lawyer rejected this call at gate " + id + ": " + gs[i].Reason
			return "blocked", id, nil
		}
		v.gated = "a lawyer's approval is pending at gate " + id + " (" + msg + ")"
		return "gated", id, nil
	}
	g := Gate{GateID: id, Kind: "hook", Hook: rule.Name, Subject: ev.subject, Detail: truncate(msg+"\n"+ev.text, 2000)}
	v.gated = "a lawyer must approve this call first: gate " + id + " is open (" + msg + "); the run pauses there until the lawyer decides"
	if err := h.e.write(ctx, taskID, kGate, id, g, false); err != nil {
		return "", "", err
	}
	h.r.hold(id) // the step commits with the gate; the run's next step pauses it
	return "gated", id, nil
}

func (h hookRunner) webhook(ctx context.Context, rule HookRule, ev event, taskID string) (string, string, error) {
	w := rule.Action.Webhook
	body, _ := json.Marshal(map[string]any{"hook": rule.Name, "event": ev.name, "taskId": taskID, "subject": ev.subject, "text": ev.text, "at": time.Now().UTC().Format(time.RFC3339)})
	sec, err := h.e.secretValue(ctx, w.Auth)
	if err != nil {
		return "", "", err
	}
	send := func(ctx context.Context) error {
		a, err := h.e.remote.do(ctx, "POST", w.URL, http.Header{"Content-Type": {"application/json"}}, body, sec)
		if err == nil && a.status/100 != 2 {
			err = fmt.Errorf("remote: the webhook answered HTTP %d", a.status)
		}
		return err
	}
	if !w.Wait {
		go func() { // fire and forget: never holds the run, its answer only logged
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			status := "delivered"
			if err := send(ctx); err != nil {
				status = err.Error()
			}
			h.e.log.Info("hook: webhook", "task", taskID, "rule", rule.Name, "host", hostOf(w.URL), "status", status)
		}()
		return "sent", hostOf(w.URL), nil
	}
	wctx, cancel := withRemoteDeadline(ctx)
	defer cancel()
	if err := send(wctx); err != nil {
		return "", "", err
	}
	return "sent", hostOf(w.URL), nil
}

// Before is the runtime's hook before a call.
func (h hookRunner) Before(ctx context.Context, p agent.Pending) (*agent.Refusal, agent.Usage) {
	if p.Spec.Final { // finish: task_finish is its hook point (a forced finish must never be refused)
		return nil, agent.Usage{}
	}
	var doc map[string]any
	_ = json.Unmarshal(p.Input, &doc)
	v, err := h.run(ctx, event{"before_call", p.Spec.Name, doc, truncate(string(p.Input), 8000), h.r.t.DocumentIDs}, h.sp.rules("before_call"), p.Budget, p.TaskID)
	switch {
	case err != nil:
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "a hook's record could not be written", Next: "try again"}, v.usage
	case v.blocked != "":
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: v.blocked, Next: "do the work without this call, or disclose what it would have found"}, v.usage
	case v.gated != "":
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: v.gated, Next: "wait: you will be told the lawyer's decision; an approved call goes through when you make it again with the same input"}, v.usage
	}
	return nil, v.usage
}

// After is the runtime's hook after a call: block or a failing-closed check withholds its answer.
func (h hookRunner) After(ctx context.Context, p agent.Pending, res agent.Result, err error) (agent.Usage, error) {
	if err != nil || p.Spec.Final {
		return agent.Usage{}, err
	}
	var in, out any
	_ = json.Unmarshal(p.Input, &in)
	_ = json.Unmarshal(res.Output, &out)
	doc := map[string]any{"input": in, "output": out}
	v, herr := h.run(ctx, event{"after_call", p.Spec.Name, doc, truncate(string(res.Output), 8000), h.r.t.DocumentIDs}, h.sp.rules("after_call"), p.Budget, p.TaskID)
	switch {
	case herr != nil:
		return v.usage, herr
	case v.blocked != "":
		return v.usage, agent.Fail(p.Spec.Name + "'s answer was withheld: " + v.blocked)
	}
	return v.usage, nil
}

// findingHooks runs finding_admitted's rules on a finding about to be recorded: block quarantines
// it (never in the memo), approve gates it (its own finding gate opens when a section cites it),
// annotate and limitation add a Limitations line.
func (h hookRunner) findingHooks(ctx context.Context, c agent.Call, b *agent.Budget, f *Finding) (agent.Usage, error) {
	rules := h.sp.rules("finding_admitted")
	if len(rules) == 0 {
		return agent.Usage{}, nil
	}
	var docs []string
	for _, q := range f.Quotes {
		docs = append(docs, q.DocumentID)
	}
	doc := map[string]any{"claim": f.Claim, "issueId": f.IssueID, "verified": f.Verified, "low": f.Low, "documents": toAny(docs)}
	text, _ := json.Marshal(map[string]any{"claim": f.Claim, "issueId": f.IssueID, "quotes": f.Quotes})
	v, err := h.run(ctx, event{"finding_admitted", f.Arm.Tool, doc, truncate(string(text), 8000), c.DocumentIDs}, rules, b, c.TaskID)
	if err != nil {
		return v.usage, err
	}
	if v.blocked != "" {
		f.Verified = false
		f.ValueNotes = append(f.ValueNotes, "quarantined: "+v.blocked)
	}
	if v.gated != "" {
		f.Low = true // the finding gate (G7) opens for it at finish
		f.ValueNotes = append(f.ValueNotes, "a lawyer must approve it: "+v.gated)
	}
	return v.usage, nil
}

func toAny(s []string) []any {
	out := []any{}
	for _, x := range s {
		out = append(out, x)
	}
	return out
}

// finishHooks runs task_finish's rules before the deliverable is rendered: block refuses the submit
// (its reason in finish's errors), the others add Limitations lines.
func (h hookRunner) finishHooks(ctx context.Context, taskID string, b *agent.Budget, stop string) (hookVerdict, error) {
	rules := h.sp.rules("task_finish")
	if len(rules) == 0 {
		return hookVerdict{}, nil
	}
	secs, err := load[Section](ctx, h.e, taskID, kSection)
	if err != nil {
		return hookVerdict{}, err
	}
	var text strings.Builder
	for _, s := range secs {
		text.WriteString(s.Markdown + "\n\n")
	}
	return h.run(ctx, event{"task_finish", "deliverable", map[string]any{"stop": stop}, truncate(text.String(), 8000), h.r.t.DocumentIDs}, rules, b, taskID)
}

// validateHookRule checks a configurable hook's file (the whole set's references are checked by
// validateHookRefs once the set loads).
func (e *Engine) validateHookRule(f, text string, bad func(string, string, ...any)) {
	var r HookRule
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil || d.More() {
		bad(f, "not one well-formed hook (fields: name, enabled, order, event, match, when, action, failure): %v", cmpErr(err, errors.New("more after the object")))
		return
	}
	name := strings.TrimSuffix(path.Base(f), ".json")
	switch {
	case r.Name != name:
		bad(f, "name: must be %q (the file's)", name)
	case slices.ContainsFunc(hooks, func(h Hook) bool { return h.Name == name }):
		bad(f, "name: %q is a Go hook's; pick another", name)
	case !slices.Contains(hookEvents, r.Event):
		bad(f, "event: one of %s", strings.Join(hookEvents, ", "))
	case !slices.Contains(hookActions[r.Event], r.Action.Type):
		bad(f, "action.type: %s takes %s", r.Event, strings.Join(hookActions[r.Event], ", "))
	case r.Failure != "closed" && r.Failure != "open":
		bad(f, "failure: closed (a failing hook blocks) or open (it lets the event through)")
	case r.Order < -1000 || r.Order > 1000:
		bad(f, "order: -1000 to 1000 (lower runs first)")
	case len(r.Action.Message) > 2000 || strings.ContainsFunc(r.Action.Message, func(r rune) bool { return unicode.IsControl(r) && r != '\n' || unicode.Is(unicode.Cf, r) }):
		bad(f, "action.message: at most 2000 characters, no control or format characters but line breaks (the lead and the Limitations read it)")
	case r.Action.Type == "check" && r.Action.Agent == "":
		bad(f, "action.agent: the subagent that checks")
	case r.Action.Type == "webhook" && r.Action.Webhook == nil:
		bad(f, "action.webhook: its url (and secret, header, prefix, wait)")
	case r.Action.Type != "webhook" && r.Action.Webhook != nil || r.Action.Type != "check" && r.Action.Agent != "":
		bad(f, "action: agent is a check's, webhook a webhook's")
	case (r.Event == "finding_admitted" || r.Event == "task_finish") && r.Match != "" && r.Match != "*" && r.Event == "task_finish":
		bad(f, "match: task_finish matches nothing (use \"*\")")
	case r.Match != "" && r.Match != "*" && !regexp.MustCompile(`^[a-z][a-z0-9_]*\*?$`).MatchString(r.Match):
		bad(f, "match: a tool or subagent name, a prefix ending in *, or *")
	}
	if r.When != nil && (!fieldPath.MatchString(r.When.Field) || !slices.Contains(condOps, r.When.Op)) {
		bad(f, "when: a field path (a.b) and an op (%s)", strings.Join(condOps, ", "))
	}
	if w := r.Action.Webhook; w != nil {
		if err := e.remote.checkURL(w.URL); err != nil {
			bad(f, "action.webhook.url: %s", strings.TrimPrefix(err.Error(), "remote: "))
		}
		validateAuth(f, "action.webhook.", w.Auth, bad)
	}
}

// validateHookRefs checks what rules name exists in the set: an exact match a tool or subagent, a
// check's subagent one whose input takes {event, subject, text} and whose output holds verdict and
// reason.
func validateHookRefs(sp *Specs, bad func(string, string, ...any)) {
	for _, n := range slices.Sorted(maps.Keys(sp.hookRules)) {
		r, f := sp.hookRules[n], hooksDir+n+".json"
		if m := r.Match; m != "" && m != "*" && !strings.HasSuffix(m, "*") && r.Event != "task_finish" {
			if _, ok := sp.specs[m]; !ok {
				bad(f, "match: %q is not a tool or subagent of this set", m)
			}
		}
		if r.Action.Type != "check" {
			continue
		}
		s, ok := sp.specs[r.Action.Agent]
		if !ok || s.Kind != "subagent" {
			bad(f, "action.agent: %q is not a subagent of this set", r.Action.Agent)
			continue
		}
		in, ierr := agent.Compile(s.In)
		out, oerr := agent.Compile(s.Out)
		ex, _ := json.Marshal(checkIn)
		switch {
		case ierr != nil || in.Validate(ex) != nil:
			bad(f, "action.agent: %s's input schema must take {event, subject, text} (three strings): a check is sent those", r.Action.Agent)
		case oerr != nil || out.Properties["verdict"] == nil || out.Properties["reason"] == nil:
			bad(f, "action.agent: %s's output schema must hold verdict (allow, block or annotate) and reason", r.Action.Agent)
		}
	}
}
