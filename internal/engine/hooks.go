// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// The Go hooks (README § Manage, Hooks): the engine code around some tools and subagents (hooked in
// register.go, sheets, aliasJoin). They are compiled code, so Manage shows them and never edits them:
// what each does, the tool it wraps, and its source, cut from the files embedded here.

//go:embed register.go
var srcRegister string

//go:embed deviation.go
var srcDeviation string

//go:embed render.go
var srcRender string

// Hook is one Go hook as Manage shows it.
type Hook struct {
	Name   string       `json:"name"`
	Wraps  string       `json:"wraps"`
	What   string       `json:"what"`
	Source []HookSource `json:"source"`
}

// HookSource is one Go function behind a hook, with its doc comment.
type HookSource struct {
	File string `json:"file"` // repo path
	Func string `json:"func"`
	Text string `json:"text"`
}

type hookDef struct {
	Name, Wraps, What string
	funcs             []string // file:func, the method as Recv.Name
}

var hookDefs = []hookDef{
	{"extract_facts", "extract_facts", "Before the subagent: sends it the section's text it reads (a page, or a run of top-level sections) and, when the call names no issue, the plan's issues. After it: admits each finding only when Go verifies every quote through the ladder (G2/G3; unverified findings are quarantined), keeps a value only when its own quote states it as the figure reader reads it, and merges exact duplicates.",
		[]string{"register.go:goTurns", "register.go:hooked.Spec", "register.go:hooked.Call", "register.go:Engine.admit", "register.go:applyReading"}},
	{"analyze_issue", "analyze_issue", "Before the subagent: appends the task's jurisdiction packs to packText. After it: admits its findings as extract_facts' are, notes authorities not in the record, and keeps the analysis (rule, application, conclusion) for the issue's drafter.",
		[]string{"register.go:hooked.Call", "register.go:Engine.admit"}},
	{"check_deviation", "check_deviation", "Before the subagent: verifies the requirement verbatim in requirementDocId, reads it in its window of text, finds the reviewed documents' nearest passages, and asks one critic call whether the document imposes the requirement as quoted (no fails the call). After it: checks every verdict's quotes and keeps a correction's values only when the quotes state them.",
		[]string{"register.go:hooked.Call", "deviation.go:Engine.quotedRequirement", "deviation.go:Engine.checkSection", "deviation.go:Engine.deviations"}},
	{"draft_section", "draft_section", "Before the subagent: builds its whole request from the ledger (the issue's verified findings, handles, worksheets, analysis and the checks a redraft fails). After it: has the figure reader read the section and saves it only as Go traces it (G8), so a figure no cited quote states is flagged.",
		[]string{"register.go:hooked.Spec", "register.go:hooked.Call", "render.go:Engine.draftRequest", "render.go:Engine.saveSection"}},
	{"numbers", "numbers", "Keeps every calc worksheet in the ledger (the deliverable's totals and computed dates trace to one) and answers op handles from the run's kept codenames.",
		[]string{"register.go:sheets.Call"}},
	{"join_discrepancies", "join_discrepancies", "Keeps the aliases the lead passes for the run, so finish's own join unifies the same parties.",
		[]string{"register.go:aliasJoin.Call"}},
}

// hooks are the hooks with their source (cut once, at start).
var hooks = func() []Hook {
	files := map[string]string{"register.go": srcRegister, "deviation.go": srcDeviation, "render.go": srcRender}
	var out []Hook
	for _, d := range hookDefs {
		h := Hook{Name: d.Name, Wraps: d.Wraps, What: d.What, Source: []HookSource{}}
		for _, fn := range d.funcs {
			file, name, _ := strings.Cut(fn, ":")
			h.Source = append(h.Source, HookSource{engineDir + file, name, funcSource(files[file], name)})
		}
		out = append(out, h)
	}
	return out
}()

func hookNamed(name string) Hook {
	for _, h := range hooks {
		if h.Name == name {
			return h
		}
	}
	return Hook{Name: name, Source: []HookSource{}}
}

// funcSource is the declaration of name (Recv.Name for a method) in src with its doc comment, or "".
func funcSource(src, name string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return ""
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || declName(fd) != name {
			continue
		}
		start := fd.Pos()
		if fd.Doc != nil {
			start = fd.Doc.Pos()
		}
		return src[fset.Position(start).Offset:fset.Position(fd.End()).Offset]
	}
	return ""
}

func declName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
