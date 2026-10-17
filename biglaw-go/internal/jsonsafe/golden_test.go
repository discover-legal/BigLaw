// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package jsonsafe_test

import (
	"bytes"
	stdjson "encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/billing"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/jsonsafe"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/precedent"
	"github.com/discover-legal/biglaw-go/internal/queue"
	"github.com/discover-legal/biglaw-go/internal/redline"
	"github.com/discover-legal/biglaw-go/internal/redtime"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/tools"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// apiTypes is every exported struct in internal/types (completeness is
// checked by TestGoldenCoversAllTypes) plus the response types other
// packages hand straight to c.JSON.
var apiTypes = []reflect.Type{
	reflect.TypeFor[types.AgentDefinition](),
	reflect.TypeFor[types.Claim](),
	reflect.TypeFor[types.Controversy](),
	reflect.TypeFor[types.RoundGoal](),
	reflect.TypeFor[types.NeedDescriptor](),
	reflect.TypeFor[types.OfferDescriptor](),
	reflect.TypeFor[types.CommunicationEdge](),
	reflect.TypeFor[types.AgentMessage](),
	reflect.TypeFor[types.RoundState](),
	reflect.TypeFor[types.StarvedRound](),
	reflect.TypeFor[types.IntraRoundMemory](),
	reflect.TypeFor[types.MemoryEntry](),
	reflect.TypeFor[types.Citation](),
	reflect.TypeFor[types.Challenge](),
	reflect.TypeFor[types.VerificationCheck](),
	reflect.TypeFor[types.VerificationResult](),
	reflect.TypeFor[types.Finding](),
	reflect.TypeFor[types.GateRequest](),
	reflect.TypeFor[types.ClientVoiceEntry](),
	reflect.TypeFor[types.ClientVoice](),
	reflect.TypeFor[types.MatterNotification](),
	reflect.TypeFor[types.EstimateWindow](),
	reflect.TypeFor[types.TaskQueueInfo](),
	reflect.TypeFor[types.NosLegalTags](),
	reflect.TypeFor[types.TaskTable](),
	reflect.TypeFor[types.Task](),
	reflect.TypeFor[types.TimeEntry](),
	reflect.TypeFor[types.ModeCapabilities](),
	reflect.TypeFor[types.ToneProfile](),
	reflect.TypeFor[types.LawyerProfile](),
	reflect.TypeFor[types.SessionUser](),
	reflect.TypeFor[types.ClientMatter](),
	reflect.TypeFor[types.Client](),
	reflect.TypeFor[types.ConflictCheckResult](),
	reflect.TypeFor[types.ConflictReport](),
	reflect.TypeFor[types.Document](),
	reflect.TypeFor[types.Attachment](),
	reflect.TypeFor[types.SearchResult](),
	reflect.TypeFor[types.TaskTemplate](),
	reflect.TypeFor[types.CitationTreatment](),
	reflect.TypeFor[types.CitationCheckResult](),
	reflect.TypeFor[types.MatterRiskFactor](),
	reflect.TypeFor[types.MatterHealthDimensions](),
	reflect.TypeFor[types.MatterHealthScore](),
	reflect.TypeFor[types.PortfolioHealthSummary](),
	reflect.TypeFor[types.PlaybookEntry](),
	reflect.TypeFor[types.Playbook](),
	reflect.TypeFor[types.InvoiceLineItem](),
	reflect.TypeFor[types.InvoiceViolation](),
	reflect.TypeFor[types.InvoiceValidationResult](),
	reflect.TypeFor[types.Headnote](),
	reflect.TypeFor[types.HeadnoteReport](),
	reflect.TypeFor[types.OcgMechCheck](),
	reflect.TypeFor[types.OcgRuleStat](),
	reflect.TypeFor[types.OcgRule](),
	reflect.TypeFor[types.OcgDocument](),
	reflect.TypeFor[types.OcgSuggestion](),
	reflect.TypeFor[types.Job](),
	reflect.TypeFor[types.WatchedDocket](),
	reflect.TypeFor[types.DocketAlert](),
	reflect.TypeFor[types.RegulationAlert](),
	reflect.TypeFor[types.BriefingMatterSnapshot](),
	reflect.TypeFor[types.BriefingBillingSnapshot](),
	reflect.TypeFor[types.ClientBriefing](),
	reflect.TypeFor[types.BudgetAlert](),
	reflect.TypeFor[types.BudgetBurn](),
	reflect.TypeFor[types.BudgetPrediction](),
	reflect.TypeFor[types.StatusReport](),
	reflect.TypeFor[types.LPMWorkstream](),
	reflect.TypeFor[types.LPMRisk](),
	reflect.TypeFor[types.LPMDeltas](),
	reflect.TypeFor[types.MatterStatusReport](),
	reflect.TypeFor[types.DeadlineRule](),
	reflect.TypeFor[types.JurisdictionRules](),
	reflect.TypeFor[types.ComputedDeadline](),
	reflect.TypeFor[types.DeadlineResult](),
	reflect.TypeFor[tools.ReviewRecord](),
	reflect.TypeFor[redtime.Timeline](),
	reflect.TypeFor[settings.PublicSettings](),
	reflect.TypeFor[cost.CostSummary](),
	reflect.TypeFor[cost.CostEntry](),
	reflect.TypeFor[audit.AuditEntry](),
	reflect.TypeFor[billing.PreBill](),
	reflect.TypeFor[precedent.Document](),
	reflect.TypeFor[redline.Report](),
	reflect.TypeFor[playbook.PlaybookQueryResult](),
	reflect.TypeFor[queue.QueueStats](),
}

// fill populates every settable field reachable from v (to a bounded depth,
// which also stops recursive types) so that no collection is nil.
func fill(v reflect.Value, depth int) {
	if !v.CanSet() {
		return
	}
	t := v.Type()
	if depth > 10 { // stop recursion, but leave no nil collections behind
		switch t.Kind() {
		case reflect.Slice:
			v.Set(reflect.MakeSlice(t, 0, 0))
		case reflect.Map:
			v.Set(reflect.MakeMap(t))
		case reflect.Struct:
			for i := range t.NumField() {
				if t.Field(i).IsExported() {
					fill(v.Field(i), depth+1)
				}
			}
		}
		return
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	case t == reflect.TypeFor[stdjson.RawMessage]():
		v.SetBytes([]byte(`{"raw":[1,"<b>&"]}`))
		return
	}
	switch t.Kind() {
	case reflect.String:
		v.SetString("s<&>\u2028" + t.Name())
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(0.125)
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		fill(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 2, 2)
		for i := range 2 {
			fill(s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), depth+1)
		}
	case reflect.Map:
		m := reflect.MakeMap(t)
		for _, k := range []string{"b", "a"} {
			key := reflect.New(t.Key()).Elem()
			if t.Key().Kind() == reflect.String {
				key.SetString(k)
			} else {
				fill(key, depth+1)
			}
			val := reflect.New(t.Elem()).Elem()
			fill(val, depth+1)
			m.SetMapIndex(key, val)
		}
		v.Set(m)
	case reflect.Interface:
		if t.NumMethod() == 0 {
			v.Set(reflect.ValueOf(map[string]any{"k": []any{"x", 1.5, nil}}))
		}
	case reflect.Struct:
		for i := range t.NumField() {
			if t.Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	}
}

// Populated values: output must be byte-identical to encoding/json —
// field names, omitempty, ,string, HTML escaping, map ordering, MarshalJSON.
func TestGoldenPopulatedMatchesEncodingJSON(t *testing.T) {
	for _, typ := range apiTypes {
		v := reflect.New(typ)
		fill(v.Elem(), 0)
		for _, val := range []any{v.Interface(), v.Elem().Interface()} {
			want, werr := stdjson.Marshal(val)
			got, gerr := jsonsafe.Marshal(val)
			if (werr != nil) != (gerr != nil) {
				t.Errorf("%v: error mismatch std=%v ours=%v", typ, werr, gerr)
				continue
			}
			if !bytes.Equal(want, got) {
				t.Errorf("%v: output differs %s", typ, firstDiff(want, got))
			}
		}
		wantI, _ := stdjson.MarshalIndent(v.Interface(), ">", "  ")
		gotI, _ := jsonsafe.MarshalIndent(v.Interface(), ">", "  ")
		if !bytes.Equal(wantI, gotI) {
			t.Errorf("%v: indented output differs %s", typ, firstDiff(wantI, gotI))
		}
	}
}

// Zero values: identical to encoding/json except that every null which
// encoding/json emitted for a nil collection is now [] or {}.
func TestGoldenZeroValuesOnlyNilCollectionsDiffer(t *testing.T) {
	for _, typ := range apiTypes {
		val := reflect.New(typ).Interface()
		want, _ := stdjson.Marshal(val)
		got, err := jsonsafe.Marshal(val)
		if err != nil {
			t.Fatalf("%v: %v", typ, err)
		}
		var w, g any
		if err := stdjson.Unmarshal(want, &w); err != nil {
			t.Fatal(err)
		}
		if err := stdjson.Unmarshal(got, &g); err != nil {
			t.Fatalf("%v: invalid JSON %s", typ, got)
		}
		compareNullAsEmpty(t, typ.String(), w, g)
	}
}

func compareNullAsEmpty(t *testing.T, path string, std, ours any) {
	t.Helper()
	switch s := std.(type) {
	case nil:
		switch o := ours.(type) {
		case nil:
		case []any:
			if len(o) != 0 {
				t.Errorf("%s: null became non-empty %v", path, o)
			}
		case map[string]any:
			if len(o) != 0 {
				t.Errorf("%s: null became non-empty %v", path, o)
			}
		case string:
			if o != "" { // nil []byte → "" (empty base64)
				t.Errorf("%s: null became %q", path, o)
			}
		default:
			t.Errorf("%s: null became %v", path, o)
		}
	case map[string]any:
		o, ok := ours.(map[string]any)
		if !ok || len(o) != len(s) {
			t.Errorf("%s: object shape differs: %v vs %v", path, s, ours)
			return
		}
		for k := range s {
			compareNullAsEmpty(t, path+"."+k, s[k], o[k])
		}
	case []any:
		o, ok := ours.([]any)
		if !ok || len(o) != len(s) {
			t.Errorf("%s: array differs", path)
			return
		}
		for i := range s {
			compareNullAsEmpty(t, path, s[i], o[i])
		}
	default:
		if !reflect.DeepEqual(std, ours) {
			t.Errorf("%s: %v != %v", path, std, ours)
		}
	}
}

// Adding a struct to internal/types must add it to apiTypes.
func TestGoldenCoversAllTypes(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob(filepath.Join("..", "types", "*.go"))
	var declared []string
	for _, f := range files {
		if filepath.Ext(f) != ".go" || len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts := sp.(*ast.TypeSpec)
				if _, ok := ts.Type.(*ast.StructType); ok && ts.Name.IsExported() {
					declared = append(declared, ts.Name.Name)
				}
			}
		}
	}
	have := map[string]bool{}
	for _, typ := range apiTypes {
		if typ.PkgPath() == reflect.TypeFor[types.Task]().PkgPath() {
			have[typ.Name()] = true
		}
	}
	var missing []string
	for _, n := range declared {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("add to apiTypes: %v", missing)
	}
}

func firstDiff(a, b []byte) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(0, i-60)
	return "at byte " + strconv.Itoa(i) + "\nstd:  …" + string(a[lo:min(len(a), i+60)]) + "\nours: …" + string(b[lo:min(len(b), i+60)])
}

func benchTask() any {
	v := reflect.New(reflect.TypeFor[types.Task]())
	fill(v.Elem(), 0)
	return v.Interface()
}

func BenchmarkTaskEncodingJSON(b *testing.B) {
	v := benchTask()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = stdjson.Marshal(v)
	}
}

func BenchmarkTaskJSONSafe(b *testing.B) {
	v := benchTask()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = jsonsafe.Marshal(v)
	}
}
