// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Manage (README § Manage): the catalogue of what a run uses (agents, tools, packs, skills, hooks)
// with each item's contents, where it is used and its recent usage; admins' edits as versions
// (spec_versions, migration 0008: a save, a revert, a skill created or deleted), each checked
// strictly before it is stored (validate: well-formed, the agent runtime's own registration rules
// over the whole set a task would run, and compatibility with what Go reads and writes); history,
// diffs, revert and an export of the tuned files as a patch or at their repo paths. The API decides
// who may write (admins); every write here records its author.

// ErrInvalid (422) is a save Manage refuses: its InvalidError lists each problem.
var ErrInvalid = errors.New("engine: invalid")

// Problem is one reason a save is refused, by file (repo path; "" for the item as a whole).
type Problem struct {
	File    string `json:"file"`
	Message string `json:"message"`
}

// InvalidError is a refused save: every problem found, not only the first.
type InvalidError struct{ Problems []Problem }

func (e *InvalidError) Error() string {
	var b []string
	for _, p := range e.Problems {
		b = append(b, strings.TrimPrefix(p.File+": ", ": ")+p.Message)
	}
	return "engine: " + strings.Join(b, "; ")
}

func (e *InvalidError) Unwrap() error { return ErrInvalid }

// Item is one entry of the catalogue.
type Item struct {
	ID       string     `json:"id"`      // kind.name: agent.read_figures, prompt.lead, pass.sweep, tool.numbers, pack.jurisdiction.us, skill.deadlines, hook.draft_section
	Section  string     `json:"section"` // agents, tools, packs, skills or hooks
	Kind     string     `json:"kind"`    // prompt, subagent, pass, tool, pack, skill or hook
	Name     string     `json:"name"`
	Summary  string     `json:"summary"`
	Editable bool       `json:"editable"`
	Builtin  bool       `json:"builtin"` // it ships in the binary (a skill an admin created does not)
	Version  int        `json:"version"` // the version new tasks run on (0: the built-in)
	Latest   int        `json:"latest"`  // the item's latest version row (a save's base)
	Deleted  bool       `json:"deleted"` // a deleted skill (its tombstone is the latest version)
	Status   string     `json:"status,omitempty"`
	Files    []string   `json:"files"`  // repo paths
	Uses     []string   `json:"uses"`   // the tools an agent may call, a skill's tools, the agent a hook wraps
	UsedBy   []string   `json:"usedBy"` // who calls it ("Go: …" for engine code)
	Usage    *ItemUsage `json:"usage"`  // recent tasks' steps (nil: none)
}

// ItemUsage is an item's lead steps over the most recent usageTasks tasks (task_steps): a call a
// fan-out makes (sweep's extract_facts, committee's critics) counts under the fan-out's step.
type ItemUsage struct {
	Tasks    int     `json:"tasks"`
	Steps    int     `json:"steps"`
	Calls    int     `json:"calls"`
	USD      float64 `json:"usd"`
	Unpriced int     `json:"unpriced"`
}

const usageTasks = 20

// ItemDetail is an item with its contents: its files now (what new tasks run on) and built in.
type ItemDetail struct {
	Item
	Content  map[string]string `json:"content"`
	Original map[string]string `json:"original"` // the built-in files ({} for a skill an admin created)
	Hook     *Hook             `json:"hook,omitempty"`
	Callers  []string          `json:"callers"` // tools a skill's tools list may name (what use_skill's callers may call)
}

// Version is one version of an item: a save, a revert (restored: the version it restored, 0 the
// built-in), a skill created or deleted. Files nil: the built-in again; {}: deleted.
type Version struct {
	Item      string            `json:"item"`
	Version   int               `json:"version"`
	Action    string            `json:"action"`
	Files     map[string]string `json:"files,omitempty"`
	Builtin   bool              `json:"builtin"` // files are the built-in's
	Deleted   bool              `json:"deleted"`
	From      int               `json:"from"`
	Restored  *int              `json:"restored"`
	Author    string            `json:"author"`
	Note      string            `json:"note"`
	CreatedAt time.Time         `json:"createdAt"`
	Event     bool              `json:"event,omitempty"` // a change log entry that is not a version (a secret set or deleted, a Test call)
}

// SaveIn is a new version an admin saves: the item's files (all of them), the version it was
// edited from (its Latest when loaded: a stale base is a conflict) and an optional note.
type SaveIn struct {
	Base  int               `json:"base"`
	Files map[string]string `json:"files"`
	Note  string            `json:"note"`
}

// latest is every item's latest version row: its number and files (NULL: the built-in).
// querier is the database or a transaction (a save reads, validates and writes under one lock).
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (e *Engine) latest(ctx context.Context, db querier) (map[string]Override, map[string]bool, error) {
	q, err := db.QueryContext(ctx, `SELECT DISTINCT ON (item) item, version, files FROM spec_versions ORDER BY item, version DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer q.Close()
	out, nulls := map[string]Override{}, map[string]bool{}
	for q.Next() {
		var id string
		var o Override
		var files []byte
		if err := q.Scan(&id, &o.Version, &files); err != nil {
			return nil, nil, err
		}
		if files == nil {
			nulls[id] = true
		} else if err := json.Unmarshal(files, &o.Files); err != nil {
			return nil, nil, err
		}
		out[id] = o
	}
	return out, nulls, q.Err()
}

// current is the snapshot a task submitted now would take (snapshotSQL's), and each item's latest row.
func (e *Engine) current(ctx context.Context, db querier) (Snapshot, map[string]Override, error) {
	rows, nulls, err := e.latest(ctx, db)
	snap := Snapshot{}
	for id, o := range rows {
		if !nulls[id] {
			snap[id] = o
		}
	}
	return snap, rows, err
}

// currentSpecs is current's snapshot parsed as the set a task submitted now would run on.
func (e *Engine) currentSpecs(ctx context.Context, db querier) (*Specs, Snapshot, map[string]Override, error) {
	snap, rows, err := e.current(ctx, db)
	if err != nil {
		return nil, nil, nil, err
	}
	sp, err := newSpecs(snap)
	return sp, snap, rows, err
}

// Items is the catalogue: every built-in item, every item an admin created, and deleted skills.
func (e *Engine) Items(ctx context.Context) ([]Item, error) {
	items, _, err := e.items(ctx)
	return items, err
}

func (e *Engine) items(ctx context.Context) ([]Item, *Specs, error) {
	sp, _, rows, err := e.currentSpecs(ctx, e.db)
	if err != nil {
		return nil, nil, err
	}
	use, err := e.usage(ctx)
	if err != nil {
		return nil, nil, err
	}
	return catalog(sp, rows, use), sp, nil
}

// catalog lists sp's items (rows: each item's latest version; use: recent usage by tool name).
func catalog(sp *Specs, rows map[string]Override, use map[string]ItemUsage) []Item {
	ids := map[string]bool{}
	for f := range builtin {
		ids[itemOf(f)] = true
	}
	for f := range sp.files {
		ids[itemOf(f)] = true
	}
	for id := range rows {
		ids[id] = true
	}
	for _, h := range hooks {
		ids["hook."+h.Name] = true
	}
	delete(ids, "")
	var out []Item
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		out = append(out, describeItem(sp, id, rows[id], use))
	}
	order := map[string]int{"agents": 0, "tools": 1, "packs": 2, "skills": 3, "hooks": 4}
	slices.SortStableFunc(out, func(a, b Item) int { return order[a.Section] - order[b.Section] })
	return out
}

// sectionOf maps an item kind to its catalogue section.
var sectionOf = map[string]string{"prompt": "agents", "agent": "agents", "pass": "agents", "tool": "tools", "server": "tools", "pack": "packs", "skill": "skills", "hook": "hooks", "rule": "hooks"}

// goCallers are the engine's own calls of a tool or subagent (rt.Invoke in Go, and the Go code a
// prompt or pack feeds), shown as "where it is used" beside the agents whose allowed lists name it.
var goCallers = map[string][]string{
	"verify_quote": {"Go: admit (every quote's paraphrase judge)"}, "read_figures": {"Go: admission and drafted sections (figures.go)"},
	"judge_figures": {"Go: join_discrepancies, finish (judge.go)"}, "extract_facts": {"sweep"}, "analyze_issue": {"cover"}, "draft_section": {"cover"},
	"prosecute": {"committee"}, "reproduce": {"committee"}, "audit_gaps": {"committee"}, "committee": {"Go: finish, a stopped run's review"},
	"cover": {"Go: finish, a stopped run"}, "finish": {"Go: a stopped run's forced finish"}, "meta": {"Go: finish (the process note)"},
	"section_check": {"Go: check_deviation's requirement check"}, "figure_context": {}, "exact_calc": {},
}

func describeItem(sp *Specs, id string, row Override, use map[string]ItemUsage) Item {
	kind, name, _ := strings.Cut(id, ".")
	it := Item{ID: id, Kind: kind, Section: sectionOf[kind], Name: name, Editable: kind != "hook", Version: sp.versions[id], Latest: row.Version,
		Files: itemFiles(sp.files, id), Uses: []string{}, UsedBy: []string{}}
	it.Builtin = len(itemFiles(builtin, id)) > 0 || kind == "hook"
	if kind == "agent" {
		it.Kind = "subagent"
	}
	callers := func(tool string) []string {
		var by []string
		if slices.Contains(leadVisible(sp), tool) {
			by = append(by, "lead")
		}
		for _, n := range slices.Sorted(maps.Keys(sp.specs)) {
			if slices.Contains(sp.specs[n].Allowed, tool) {
				by = append(by, n)
			}
		}
		return append(by, goCallers[tool]...)
	}
	if u, ok := use[cmpStr(id == "prompt.lead", "lead", name)]; ok {
		it.Usage = &u
	}
	switch kind {
	case "prompt":
		it.Summary = firstSentence(sp.prompt(name))
		if name == "lead" {
			it.Uses = append(it.Uses, leadVisible(sp)...)
		}
		it.UsedBy = append(it.UsedBy, goCallers[name]...)
	case "agent", "pass", "tool":
		s := sp.Spec(name)
		it.Summary, it.Uses, it.UsedBy = firstSentence(s.Description), append(it.Uses, s.Allowed...), callers(name)
		if t, ok := sp.tools[name]; ok {
			it.Kind, it.Status = t.Kind+" tool", onOff(t.Enabled)
			if t.MCP != nil {
				it.Uses = append(it.Uses, "server."+t.MCP.Server)
			}
		} else if s.Name == "" && row.Version > 0 {
			it.Deleted, it.Summary = true, deletedSummary
		}
	case "server":
		if v, ok := sp.servers[name]; ok {
			it.Kind, it.Status, it.Summary = "MCP server", onOff(v.Enabled), "MCP server at "+hostOf(v.URL)+"."
			for _, n := range slices.Sorted(maps.Keys(sp.tools)) {
				if m := sp.tools[n].MCP; m != nil && m.Server == name {
					it.UsedBy = append(it.UsedBy, n)
				}
			}
		} else {
			it.Kind, it.Deleted, it.Summary = "MCP server", true, deletedSummary
		}
	case "pack":
		dir, code, _ := strings.Cut(name, ".")
		for _, p := range sp.packs {
			if p.Code == code && p.Practice == (dir == "practice") {
				it.Summary, it.Status = firstSentence(p.Body), p.Status
			}
		}
		it.UsedBy = []string{"lead (the brief)", "analyze_issue (packText)", "committee (checklist)", "finish (Limitations)"}
	case "skill":
		if k, ok := sp.skill(name); ok {
			it.Summary, it.Status, it.Uses = k.Description, k.Status, append(it.Uses, k.Tools...)
		} else {
			it.Deleted, it.Summary = true, deletedSummary
		}
		it.UsedBy = callers("use_skill")
	case "hook":
		h := hookNamed(name)
		it.Summary, it.Uses, it.UsedBy = h.What, []string{h.Wraps}, callers(h.Wraps)
	case "rule":
		it.Kind = "configurable hook"
		if r, ok := sp.hookRules[name]; ok {
			it.Status, it.Summary = onOff(r.Enabled), strings.Join(append([]string{r.summary()}, approveCallers(sp, name)...), " Note: ")
			if r.exact() && r.Event != "task_finish" {
				it.Uses = append(it.Uses, r.Match)
			}
			if r.Action.Agent != "" {
				it.Uses = append(it.Uses, r.Action.Agent)
			}
			it.UsedBy = append(it.UsedBy, map[string]string{"before_call": "every call (Runtime.Invoke)", "after_call": "every call (Runtime.Invoke)",
				"finding_admitted": "admit (each new finding)", "task_finish": "finish (before the deliverable)"}[r.Event])
		} else {
			it.Deleted, it.Summary = true, deletedSummary
		}
	}
	return it
}

const deletedSummary = "Deleted (revert to restore it)."

func onOff(on bool) string { return map[bool]string{true: "enabled", false: "disabled"}[on] }

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return "?"
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return truncate(s, 240)
}

// usage is each tool's (and the lead's) steps over the most recent usageTasks tasks.
func (e *Engine) usage(ctx context.Context) (map[string]ItemUsage, error) {
	q, err := e.db.QueryContext(ctx, `SELECT CASE WHEN s.kind = 'lead_turn' THEN 'lead' ELSE s.tool END, count(DISTINCT s.task_id), count(*),
		coalesce(sum((s.usage->>'calls')::int), 0), coalesce(sum((s.usage->>'usd')::float8), 0), coalesce(sum((s.usage->>'unpriced')::int), 0)
		FROM task_steps s JOIN (SELECT id FROM tasks ORDER BY created_at DESC LIMIT $1) t ON t.id = s.task_id
		WHERE s.status <> 'started' AND s.kind <> 'note' GROUP BY 1`, usageTasks)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := map[string]ItemUsage{}
	for q.Next() {
		var n string
		var u ItemUsage
		if err := q.Scan(&n, &u.Tasks, &u.Steps, &u.Calls, &u.USD, &u.Unpriced); err != nil {
			return nil, err
		}
		out[n] = u
	}
	return out, q.Err()
}

// ItemDetail is one item with its contents.
func (e *Engine) ItemDetail(ctx context.Context, id string) (ItemDetail, error) {
	items, sp, err := e.items(ctx)
	if err != nil {
		return ItemDetail{}, err
	}
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == id })
	if i < 0 {
		return ItemDetail{}, fmt.Errorf("%w: no item %s", ErrNotFound, id)
	}
	return detail(sp, items[i]), nil
}

// detail is it with its contents in sp.
func detail(sp *Specs, it Item) ItemDetail {
	d := ItemDetail{Item: it, Content: fileMap(sp.files, it.ID), Original: fileMap(builtin, it.ID), Callers: skillCallable(sp)}
	if it.Kind == "hook" {
		h := hookNamed(it.Name)
		d.Hook = &h
	}
	return d
}

func fileMap(files map[string][]byte, id string) map[string]string {
	out := map[string]string{}
	for _, f := range itemFiles(files, id) {
		out[f] = string(files[f])
	}
	return out
}

// skillCallable is what a skill's tools list may name: the tools of every agent that may call
// use_skill (the lead's list, and any subagent an admin allowed it), so a skill never points at a
// tool its reader cannot call.
func skillCallable(sp *Specs) []string {
	set := map[string]bool{}
	if lv := leadVisible(sp); slices.Contains(lv, "use_skill") {
		for _, t := range lv {
			set[t] = true
		}
	}
	for _, s := range sp.specs {
		if slices.Contains(s.Allowed, "use_skill") {
			for _, t := range s.Allowed {
				set[t] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// History is an item's versions, newest first (at most 100), each with its files.
func (e *Engine) History(ctx context.Context, id string) ([]Version, error) {
	return e.versions(ctx, `WHERE item = $1 ORDER BY version DESC LIMIT 100`, true, id)
}

// Changes is the change log, newest first (at most limit): every version of every item, without
// files, and the changes that are not versions (a secret set or deleted, a test call: version 0).
func (e *Engine) Changes(ctx context.Context, limit int) ([]Version, error) {
	limit = min(max(limit, 1), 500)
	vs, err := e.versions(ctx, `ORDER BY created_at DESC, item, version DESC LIMIT $1`, false, limit)
	if err != nil {
		return nil, err
	}
	q, err := e.db.QueryContext(ctx, `SELECT item, action, author, note, created_at FROM manage_events ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	for q.Next() {
		v := Version{Event: true}
		if err := q.Scan(&v.Item, &v.Action, &v.Author, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		vs = append(vs, v)
	}
	slices.SortStableFunc(vs, func(a, b Version) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return vs[:min(len(vs), limit)], q.Err()
}

func (e *Engine) versions(ctx context.Context, where string, files bool, args ...any) ([]Version, error) {
	q, err := e.db.QueryContext(ctx, `SELECT item, version, action, files, from_version, restored, author, note, created_at FROM spec_versions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := []Version{}
	for q.Next() {
		var v Version
		var raw []byte
		var restored sql.NullInt64
		if err := q.Scan(&v.Item, &v.Version, &v.Action, &raw, &v.From, &restored, &v.Author, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		if restored.Valid {
			r := int(restored.Int64)
			v.Restored = &r
		}
		v.Builtin = raw == nil
		if raw != nil {
			_ = json.Unmarshal(raw, &v.Files)
			v.Deleted = len(v.Files) == 0
		}
		switch {
		case !files:
			v.Files = nil
		case v.Builtin:
			v.Files = fileMap(builtin, v.Item)
		}
		out = append(out, v)
	}
	return out, q.Err()
}

// filesAt is an item's files at version n (0: the built-in; a revert to it: the built-in; a
// tombstone: none).
func (e *Engine) filesAt(ctx context.Context, id string, n int) (map[string]string, error) {
	if n == 0 {
		return fileMap(builtin, id), nil
	}
	files, err := versionFiles(ctx, e.db, id, n)
	if err == nil && files == nil {
		return fileMap(builtin, id), nil
	}
	return files, err
}

// versionFiles is version n's files (nil: the built-in's, its files column NULL).
func versionFiles(ctx context.Context, q querier, id string, n int) (map[string]string, error) {
	files, err := rowFiles(q.QueryRowContext(ctx, `SELECT files FROM spec_versions WHERE item = $1 AND version = $2`, id, n))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s has no version %d", ErrNotFound, id, n)
	}
	return files, err
}

// rowFiles is a spec_versions row's files (nil: NULL, the built-in's).
func rowFiles(row *sql.Row) (map[string]string, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil || raw == nil {
		return nil, err
	}
	files := map[string]string{}
	return files, json.Unmarshal(raw, &files)
}

// FileDiff is one file's unified diff between two versions of an item.
type FileDiff struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

// Diff is an item's diff from version from to version to (0: the built-in; to < 0: the current).
func (e *Engine) Diff(ctx context.Context, id string, from, to int) ([]FileDiff, error) {
	if to < 0 {
		_, rows, err := e.current(ctx, e.db)
		if err != nil {
			return nil, err
		}
		to = rows[id].Version
	}
	a, err := e.filesAt(ctx, id, from)
	if err != nil {
		return nil, err
	}
	b, err := e.filesAt(ctx, id, to)
	if err != nil {
		return nil, err
	}
	return diffFiles(a, b), nil
}

func diffFiles(a, b map[string]string) []FileDiff {
	out, paths := []FileDiff{}, maps.Clone(a)
	maps.Copy(paths, b)
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		x, inA := a[p]
		y, inB := b[p]
		if d := filePatch(p, x, y, inA, inB); d != "" {
			out = append(out, FileDiff{p, d})
		}
	}
	return out
}

// Export is the files of the overridden items (all of them, or those in ids) as they are now and
// as built in, by repo path: what a patch against the repository, or a zip of the tuned files, holds.
// A deleted skill's files are absent from now; a created skill's from built.
func (e *Engine) Export(ctx context.Context, ids []string) (now, built map[string]string, err error) {
	snap, _, err := e.current(ctx, e.db)
	if err != nil {
		return nil, nil, err
	}
	now, built = map[string]string{}, map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(snap)) {
		if len(ids) > 0 && !slices.Contains(ids, id) {
			continue
		}
		maps.Copy(now, snap[id].Files)
		maps.Copy(built, fileMap(builtin, id))
	}
	return now, built, nil
}

// Patch is the export as one git-style patch against the repository (git apply, patch -p1).
func Patch(now, built map[string]string) string {
	var b strings.Builder
	for _, d := range diffFiles(built, now) {
		b.WriteString(d.Patch)
	}
	return b.String()
}

// locked runs fn in one transaction under the Manage lock: saves, reverts and deletes are serial,
// so each validates the set the others left (two saves of different items can never make a set that
// does not load).
func (e *Engine) locked(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return e.st.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('biglaw.spec_versions'))`); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Save stores an admin's new version of an item after validate accepts it. A skill that does not
// exist is created (base 0). notes say what Go changed (a pack's or skill's status reset to candidate).
func (e *Engine) Save(ctx context.Context, id string, in SaveIn, author string) (v Version, notes []string, err error) {
	err = e.locked(ctx, func(tx *sql.Tx) error { v, notes, err = e.save(ctx, tx, id, in, author); return err })
	return v, notes, err
}

func (e *Engine) save(ctx context.Context, tx *sql.Tx, id string, in SaveIn, author string) (Version, []string, error) {
	before, snap, rows, err := e.currentSpecs(ctx, tx)
	if err != nil {
		return Version{}, nil, err
	}
	kind, name, _ := strings.Cut(id, ".")
	exists := len(itemFiles(before.files, id)) > 0
	action := "save"
	switch {
	case kind == "hook":
		return Version{}, nil, invalid("", "hooks are Go code: view only")
	case !exists && !slices.Contains([]string{"skill", "agent", "tool", "server", "rule"}, kind):
		if _, ok := rows[id]; !ok && len(itemFiles(builtin, id)) == 0 {
			return Version{}, nil, fmt.Errorf("%w: no item %s", ErrNotFound, id)
		}
	case !exists:
		action = "create"
	}
	if in.Base != rows[id].Version {
		return Version{}, nil, fmt.Errorf("%w: %s changed since you opened it (version %d is now its latest): reload it and apply your edit again", ErrConflict, id, rows[id].Version)
	}
	files := maps.Clone(in.Files)
	if files == nil {
		files = map[string]string{}
	}
	old := fileMap(before.files, id)
	if kind == "skill" && !exists && !skillName.MatchString(name) {
		return Version{}, nil, invalid("", "a skill's name is 2–40 lower-case letters, digits and -, starting with a letter")
	}
	prior := old
	if !exists && rows[id].Version > 0 { // re-created after a deletion: reviewed against what it was before it
		if prior, err = lastContent(ctx, tx, id); err != nil {
			return Version{}, nil, err
		}
	}
	notes, err := reviewRule(kind, prior, files)
	if err != nil {
		return Version{}, nil, err
	}
	if kind == "tool" {
		notes = append(notes, acceptRule(old, files, author)...)
	}
	if action == "create" && len(files) == 0 {
		return Version{}, nil, invalid("", "a new %s needs its files", kind)
	}
	if maps.Equal(old, files) {
		return Version{}, nil, invalid("", "nothing changed: the files are the version new tasks already run on")
	}
	next := maps.Clone(snap)
	next[id] = Override{Version: in.Base + 1, Files: files}
	if err := e.validate(next, id); err != nil {
		return Version{}, nil, err
	}
	if err := secretOrigins(ctx, tx, files); err != nil {
		return Version{}, nil, err
	}
	if sp, err := newSpecs(next); err == nil && kind == "rule" {
		notes = append(notes, approveCallers(sp, name)...)
	}
	v, err := e.insertVersion(ctx, tx, id, in.Base+1, action, files, rows[id].Version, nil, author, in.Note)
	return v, notes, err
}

// lastContent is an item's files at its latest version that is not a deletion (the built-in's when
// that version restored it, none when it never had any): what a re-created skill is reviewed
// against, so a deletion and a re-creation never carry a reviewer over to changed content.
func lastContent(ctx context.Context, tx *sql.Tx, id string) (map[string]string, error) {
	files, err := rowFiles(tx.QueryRowContext(ctx, `SELECT files FROM spec_versions WHERE item = $1 AND (files IS NULL OR files <> '{}'::jsonb) ORDER BY version DESC LIMIT 1`, id))
	if errors.Is(err, sql.ErrNoRows) || err == nil && files == nil {
		return fileMap(builtin, id), nil
	}
	return files, err
}

// acceptRule: an imported tool's third-party text is accepted by the admin whose save names an
// acceptor (that admin's name is recorded, whatever the file says); a save that changes the text
// and names no new acceptor clears the acceptance and disables the tool (validate then refuses
// enabling it until an admin accepts).
func acceptRule(old, files map[string]string, author string) []string {
	for f, text := range files {
		var t, was CustomTool
		if !strings.HasPrefix(f, toolsDir) || json.Unmarshal([]byte(text), &t) != nil || t.Imported == nil {
			continue
		}
		_ = json.Unmarshal([]byte(old[f]), &was)
		prev := cmpOr(was.Imported, &Imported{})
		switch {
		case t.Imported.AcceptedBy != "" && t.Imported.AcceptedBy != prev.AcceptedBy: // this save accepts the text it saves
			if t.Imported.AcceptedBy == author {
				continue
			}
			t.Imported.AcceptedBy = author
		case t.Imported.AcceptedBy != "" && (t.Imported.Text != prev.Text || !jsonEqual(t.In, was.In)): // the server's text or schema changed
			t.Imported.AcceptedBy, t.Enabled = "", false
		default:
			continue
		}
		b, _ := json.MarshalIndent(t, "", " ")
		if files[f] = string(b) + "\n"; t.Imported.AcceptedBy == "" {
			return []string{path.Base(f) + ": its server's text changed and this save accepts no new text, so it is disabled until an admin accepts it"}
		}
	}
	return nil
}

func cmpOr[T any](p *T, def *T) *T {
	if p != nil {
		return p
	}
	return def
}

// secretOrigins refuses a tool or server whose URL is not the origin its named secret is set for:
// a secret is never sent elsewhere, so moving a tool to another origin takes setting its secret
// again (value and all) for the new one. A secret not set yet is the Secrets panel's to set.
func secretOrigins(ctx context.Context, tx *sql.Tx, files map[string]string) error {
	for _, f := range slices.Sorted(maps.Keys(files)) {
		a, u := remoteOf(f, files[f])
		if a.Secret == "" {
			continue
		}
		var origin string
		err := tx.QueryRowContext(ctx, `SELECT origin FROM secrets WHERE name = $1`, a.Secret).Scan(&origin)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		if origin != originOf(u) {
			return invalid(f, "secret: %q is set for %s, not for this URL's origin %s; it is never sent to another origin: set it again for this URL in Secrets (re-entering its value), or name another secret",
				a.Secret, cmpStr(origin != "", origin, "no origin"), originOf(u))
		}
	}
	return nil
}

// remoteOf is the secret and URL an HTTP tool's, a server's or a webhook rule's file names (none for
// any other file, or one that does not decode).
func remoteOf(f, text string) (Auth, string) {
	var t CustomTool
	var v Server
	var r HookRule
	switch {
	case strings.HasPrefix(f, toolsDir) && json.Unmarshal([]byte(text), &t) == nil && t.HTTP != nil:
		return t.HTTP.Auth, t.HTTP.URL
	case strings.HasPrefix(f, serversDir) && json.Unmarshal([]byte(text), &v) == nil:
		return v.Auth, v.URL
	case strings.HasPrefix(f, hooksDir) && json.Unmarshal([]byte(text), &r) == nil && r.Action.Webhook != nil:
		return r.Action.Webhook.Auth, r.Action.Webhook.URL
	}
	return Auth{}, ""
}

// Revert stores a version that restores version to (0: the built-in; for a skill an admin
// created, its absence) after validate accepts the set it makes.
func (e *Engine) Revert(ctx context.Context, id string, to, base int, note, author string) (v Version, err error) {
	err = e.locked(ctx, func(tx *sql.Tx) error { v, err = e.revert(ctx, tx, id, to, base, note, author); return err })
	return v, err
}

func (e *Engine) revert(ctx context.Context, tx *sql.Tx, id string, to, base int, note, author string) (Version, error) {
	snap, rows, err := e.current(ctx, tx)
	if err != nil {
		return Version{}, err
	}
	if strings.HasPrefix(id, "hook.") {
		return Version{}, invalid("", "hooks are Go code: view only")
	}
	if _, ok := rows[id]; !ok {
		return Version{}, fmt.Errorf("%w: %s has no versions to revert (it is the built-in)", ErrNotFound, id)
	}
	if base != rows[id].Version {
		return Version{}, fmt.Errorf("%w: %s changed since you opened it (version %d is now its latest): reload it", ErrConflict, id, rows[id].Version)
	}
	var files map[string]string // nil: the built-in again
	if to > 0 {
		if files, err = versionFiles(ctx, tx, id, to); err != nil {
			return Version{}, err
		}
	}
	if files == nil && len(itemFiles(builtin, id)) == 0 {
		files = map[string]string{} // a created skill has no built-in: back to none
	}
	next := maps.Clone(snap)
	delete(next, id)
	if files != nil {
		next[id] = Override{Version: base + 1, Files: files}
	}
	if err := e.validate(next, id); err != nil {
		return Version{}, err
	}
	return e.insertVersion(ctx, tx, id, base+1, "revert", files, base, &to, author, note)
}

// Delete deletes a skill (a tombstone version: revertable, like any other).
func (e *Engine) Delete(ctx context.Context, id string, base int, note, author string) (v Version, err error) {
	err = e.locked(ctx, func(tx *sql.Tx) error { v, err = e.delete(ctx, tx, id, base, note, author); return err })
	return v, err
}

func (e *Engine) delete(ctx context.Context, tx *sql.Tx, id string, base int, note, author string) (Version, error) {
	kind, name, _ := strings.Cut(id, ".")
	switch {
	case kind == "prompt" || kind == "pass" || kind == "pack" || kind == "hook":
		return Version{}, invalid("", "%s is part of the engine's workflow: edit or revert it; it cannot be deleted", id)
	case kind == "agent" || kind == "tool":
		if why := goDepends(name); why != "" {
			return Version{}, invalid("", "%s cannot be deleted or disabled: %s. Edit or revert it instead", id, why)
		}
	}
	sp, snap, rows, err := e.currentSpecs(ctx, tx)
	if err != nil {
		return Version{}, err
	}
	switch {
	case len(itemFiles(sp.files, id)) == 0:
		return Version{}, fmt.Errorf("%w: %s does not exist (already deleted?)", ErrNotFound, id)
	case kind == "agent" && !strings.HasPrefix(itemFiles(sp.files, id)[0], agentsDir): // the case above: it has files
		return Version{}, invalid("", "%s is not a subagent", id)
	case base != rows[id].Version:
		return Version{}, fmt.Errorf("%w: %s changed since you opened it (version %d is now its latest): reload it", ErrConflict, id, rows[id].Version)
	}
	next := maps.Clone(snap)
	next[id] = Override{Version: base + 1, Files: map[string]string{}}
	if err := e.validate(next, id); err != nil {
		return Version{}, err
	}
	return e.insertVersion(ctx, tx, id, base+1, "delete", map[string]string{}, base, nil, author, note)
}

func (e *Engine) insertVersion(ctx context.Context, tx *sql.Tx, id string, n int, action string, files map[string]string, from int, restored *int, author, note string) (Version, error) {
	var raw any
	if files != nil {
		b, _ := json.Marshal(files)
		raw = b
	}
	v := Version{Item: id, Version: n, Action: action, From: from, Restored: restored, Author: author, Note: truncate(strings.TrimSpace(note), 2000),
		Builtin: files == nil, Deleted: files != nil && len(files) == 0}
	err := tx.QueryRowContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, restored, author, note) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at`, id, n, action, raw, from, restored, author, v.Note).Scan(&v.CreatedAt)
	var pe *pq.Error
	if errors.As(err, &pe) && pe.Code == "23505" {
		return Version{}, fmt.Errorf("%w: another save of %s came first: reload it and apply your edit again", ErrConflict, id)
	}
	if err != nil {
		return Version{}, err
	}
	e.log.Info("manage: item changed", "item", id, "action", action, "from", from, "to", n, "author", author)
	return v, nil
}

func invalid(file, format string, a ...any) error {
	return &InvalidError{[]Problem{{file, fmt.Sprintf(format, a...)}}}
}

var skillName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// reviewRule keeps the lawyer-review rule (README § Packs, § Skills): everything in a pack or skill
// is its content (every file, every front matter field but status and reviewedBy, the body), so any
// change to it resets its status to candidate and clears reviewedBy, unless the save names a reviewer
// who is not the one before (a new item: unless it names one). It rewrites the front matter in files.
func reviewRule(kind string, old, files map[string]string) ([]string, error) {
	if kind != "pack" && kind != "skill" {
		return nil, nil
	}
	head := ""
	for f := range files {
		if kind == "pack" || path.Base(f) == "SKILL.md" {
			head = f
		}
	}
	if head == "" {
		return nil, nil // a skill without its SKILL.md: validate refuses it
	}
	fields, _, _, err := frontMatter(files[head])
	if err != nil {
		return nil, invalid(head, "%v", err)
	}
	of, _, _, _ := frontMatter(old[head])
	content := func(fs map[string]string) string { // the item less its review fields, in a fixed order
		var b strings.Builder
		for _, f := range slices.Sorted(maps.Keys(fs)) {
			text := fs[f]
			if f == head {
				text = setFront(text, map[string]string{"status": "", "reviewedBy": ""})
			}
			fmt.Fprintf(&b, "%s\x00%s\x00", f, text)
		}
		return b.String()
	}
	if len(old) > 0 && content(old) == content(files) {
		return nil, nil // a status or reviewer change only: a lawyer recording the review
	}
	if fields["reviewedBy"] != "" && fields["reviewedBy"] != of["reviewedBy"] {
		return nil, nil // the save names the reviewer of what it saves
	}
	if fields["status"] == "candidate" && fields["reviewedBy"] == "" {
		return nil, nil
	}
	files[head] = setFront(files[head], map[string]string{"status": "candidate", "reviewedBy": ""})
	why := "its content changed and no new reviewer was named"
	if len(old) == 0 {
		why = "it is new and names no reviewer"
	}
	return []string{path.Base(path.Dir(head)) + "/" + path.Base(head) + ": " + why + ", so its status is candidate"}, nil
}

// setFront rewrites keys' values in text's front matter (each key's line in place).
func setFront(text string, kv map[string]string) string {
	rest, _ := strings.CutPrefix(text, "---\n")
	head, body, _ := strings.Cut(rest, "\n---\n")
	lines := strings.Split(head, "\n")
	for i, ln := range lines {
		k, _, ok := strings.Cut(ln, ":")
		if v, set := kv[strings.TrimSpace(k)]; ok && set {
			lines[i] = strings.TrimSpace(k) + ": " + v
			lines[i] = strings.TrimRight(lines[i], " ")
		}
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n" + body
}

// Builtins is the catalogue with its contents as built into this binary (no overrides, no usage):
// what the recorded demo shows read-only (eval/demo records it into ui/src/recorded/manage.json).
func Builtins() []ItemDetail {
	var out []ItemDetail
	for _, it := range catalog(builtinSpecs, nil, nil) {
		out = append(out, detail(builtinSpecs, it))
	}
	return out
}
