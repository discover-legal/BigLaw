// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"embed"
	"encoding/json"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Finding is the common finding shape (README § Tools): a claim, its quotes, typed values,
// entities and relations. Verified is false for a quarantined finding (G2, G3).
type Finding struct {
	FID       string     `json:"fid"`
	IssueID   string     `json:"issueId"`
	Claim     string     `json:"claim"`
	Verified  bool       `json:"verified"`
	Quotes    []Quote    `json:"quotes"`
	Values    []Value    `json:"values"`
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
}

type Quote struct {
	DocumentID string `json:"documentId"`
	Quote      string `json:"quote"`
	Locator    string `json:"locator"`
}

// Value is one typed figure; Class (a BELO-lite quantity class) may be empty, then it is inferred.
type Value struct {
	Entity string `json:"entity"`
	Metric string `json:"metric"`
	Value  string `json:"value"`
	Unit   string `json:"unit"`
	Date   string `json:"date"`
	Class  string `json:"class"`
	// Fig is what the figure reader read the value as (figread.go), set by admission: a value with
	// none, or an unclear one, is never compared, summed or ranked as a quantity.
	Fig *Reading `json:"figure,omitempty"`
}

type Entity struct {
	Name  string `json:"name"`
	Class Class  `json:"class"`
}

type Relation struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
}

// Source is the task's matter ledger, as these tools read it (engine's ledger, PR 4b).
type Source interface {
	Findings(ctx context.Context, taskID string) ([]Finding, error)
}

// Unswept is a Source that also knows which documents the evidence sweep left partly unread: their
// figures can be missing from the findings, so join_discrepancies names them (bench r3).
type Unswept interface {
	Unread(ctx context.Context, taskID string) ([]string, error)
}

//go:embed specs/*.json
var specs embed.FS

func spec(name string) agent.Spec {
	var s agent.Spec
	data, err := specs.ReadFile("specs/" + name + ".json")
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		panic("analysis: specs/" + name + ".json: " + err.Error())
	}
	return s
}

// Tools are numbers and join_discrepancies over src.
func Tools(src Source) []agent.Tool {
	return []agent.Tool{
		agent.GoTool{S: spec("numbers"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
			var in struct {
				Op, Fn     string
				Operands   []Operand
				Holidays   []string
				FIDs       []string `json:"fids"`
				SalientTop int
			}
			if err := agent.Decode(c, &in); err != nil {
				return nil, agent.Usage{}, err
			}
			fs, err := src.Findings(ctx, c.TaskID)
			if err != nil {
				return nil, agent.Usage{}, err
			}
			out, pg := map[string]any{"wid": nil, "result": nil, "unit": nil, "worksheet": nil, "literalOperands": nil, "handles": nil, "tableMarkdown": nil}, agent.NewPage()
			switch {
			case in.Op == "calc" && (in.Fn == "" || len(in.Operands) == 0):
				return nil, agent.Usage{}, agent.Fail(`numbers: op "calc" needs fn (sum, diff, ratio, pct, product, date_add, date_diff or business_days) and operands[] (the values to calculate on); send them and call again, e.g. ` + cmpExample("date_add"))
			case in.Op == "calc":
				w, err := Calc(fs, in.Fn, in.Operands, in.Holidays)
				if err != nil {
					return nil, agent.Usage{}, agent.Fail("numbers: " + err.Error())
				}
				out["wid"], out["result"], out["unit"], out["worksheet"], out["literalOperands"] = w.WID, w.Result, w.Unit, w.Steps, w.LiteralOperands
			case in.Op == "handles":
				out["handles"] = agent.Take(pg, Handles(fs, in.FIDs, in.SalientTop))
			default: // whole rows: a page of the table (truncated: name fewer fids)
				out["tableMarkdown"] = strings.Join(agent.Take(pg, strings.SplitAfter(KeyFigures(fs, in.FIDs), "\n")), "")
			}
			out["truncated"] = pg.Truncated
			return out, agent.Usage{}, nil
		}},
		agent.GoTool{S: spec("join_discrepancies"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
			var in struct {
				FIDs    []string `json:"fids"`
				Aliases []Alias
			}
			if err := agent.Decode(c, &in); err != nil {
				return nil, agent.Usage{}, err
			}
			fs, err := src.Findings(ctx, c.TaskID)
			if err != nil {
				return nil, agent.Usage{}, err
			}
			for _, id := range in.FIDs {
				if i := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == id }); i < 0 || !fs[i].Verified {
					return nil, agent.Usage{}, agent.Fail("join_discrepancies: " + id + " is not a verified finding of this task; name only verified findings, or none for all")
				}
			}
			// the join runs over every verified finding, so a conflict between one issue's finding and
			// another's is found when either is named (bench5 SEC: per-issue fids split each pair); fids
			// only choose which conflicts to report: those holding a named finding (null or []: all).
			// Go proposes the candidate pairs; the source's judge (the engine's judge_figures) decides
			// them, and only its "yes" is a conflict (lab-r4 r7).
			j, _ := src.(Judge)
			all, unclearAll, unjudged, u, err := Join(ctx, c, fs, j)
			if err != nil {
				return nil, u, err
			}
			named := func(vs ...string) bool {
				return len(in.FIDs) == 0 || slices.ContainsFunc(vs, func(fid string) bool { return slices.Contains(in.FIDs, fid) })
			}
			conflicts, unclear := []Conflict{}, []map[string]string{}
			for _, x := range all {
				if named(x.Values[0].FID, x.Values[1].FID) {
					conflicts = append(conflicts, x)
				}
			}
			for _, x := range unclearAll {
				if named(x.A.FID, x.B.FID) {
					unclear = append(unclear, map[string]string{"a": x.A.Ref + " " + x.A.Value, "b": x.B.Ref + " " + x.B.Value})
				}
			}
			unread := []string{}
			if u, ok := src.(Unswept); ok {
				if unread, err = u.Unread(ctx, c.TaskID); err != nil {
					return nil, agent.Usage{}, err
				}
			}
			pg := agent.NewPage() // one page (truncated: name fewer fids)
			return map[string]any{"conflicts": agent.Take(pg, conflicts), "unclear": agent.Take(pg, unclear), "notJudged": unjudged,
				"unreadDocuments": agent.Take(pg, append([]string{}, unread...)), "truncated": pg.Truncated}, u, nil
		}},
	}
}
