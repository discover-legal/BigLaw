// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A line diff for Manage (history, revert previews, the export patch): the longest common
// subsequence of two texts' lines, printed as a unified diff that git apply and patch accept; and an
// item's diff between versions and the export of the overrides over it (Diff, Export, Patch).

// diffContext is the unchanged lines a hunk shows around a change.
const diffContext = 3

// maxDiffCells bounds the LCS table; past it the diff is the whole of a replaced by the whole of b.
const maxDiffCells = 4_000_000

type edit struct {
	op   byte // ' ' same, '-' only in a, '+' only in b
	line string
}

// splitLines is text's lines, each with its "\n" (the last without one when text does not end so).
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func lineDiff(a, b []string) []edit {
	n, m := len(a), len(b)
	if n*m > maxDiffCells {
		var out []edit
		for _, l := range a {
			out = append(out, edit{'-', l})
		}
		for _, l := range b {
			out = append(out, edit{'+', l})
		}
		return out
	}
	t := make([][]int32, n+1) // t[i][j]: the LCS length of a[i:] and b[j:]
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	var out []edit
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out, i, j = append(out, edit{' ', a[i]}), i+1, j+1
		case i < n && (j == m || t[i+1][j] >= t[i][j+1]): // a removal before the addition that replaces it
			out, i = append(out, edit{'-', a[i]}), i+1
		default:
			out, j = append(out, edit{'+', b[j]}), j+1
		}
	}
	return out
}

// unifiedHunks is the "@@" hunks turning a into b ("" when they are the same).
func unifiedHunks(a, b string) string {
	es := lineDiff(splitLines(a), splitLines(b))
	var out strings.Builder
	for k := 0; k < len(es); {
		if es[k].op == ' ' {
			k++
			continue
		}
		start := max(0, k-diffContext) // a hunk: context, changes (merging those within 2×context), context
		end := k
		for end < len(es) {
			if es[end].op != ' ' {
				end++
				continue
			}
			run := end
			for run < len(es) && es[run].op == ' ' {
				run++
			}
			if run == len(es) || run-end > 2*diffContext {
				end = min(run, end+diffContext)
				break
			}
			end = run
		}
		ai, bi := 0, 0 // the hunk's first line in a and b (1-based), and its lengths
		for _, e := range es[:start] {
			ai, bi = ai+btoi(e.op != '+'), bi+btoi(e.op != '-')
		}
		al, bl := 0, 0
		for _, e := range es[start:end] {
			al, bl = al+btoi(e.op != '+'), bl+btoi(e.op != '-')
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(ai, al), hunkRange(bi, bl))
		for _, e := range es[start:end] {
			out.WriteByte(e.op)
			out.WriteString(strings.TrimSuffix(e.line, "\n"))
			out.WriteByte('\n')
			if !strings.HasSuffix(e.line, "\n") {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
		k = end
	}
	return out.String()
}

func hunkRange(before, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	if n == 1 {
		return fmt.Sprint(before + 1)
	}
	return fmt.Sprintf("%d,%d", before+1, n)
}

// filePatch is one file's git-style patch from a to b at the repo path p; exists says whether each
// side has the file (a new file, a deleted one). "" when nothing changed.
func filePatch(p, a, b string, aExists, bExists bool) string {
	h := unifiedHunks(a, b)
	if h == "" && aExists == bExists {
		return ""
	}
	from, to := "a/"+p, "b/"+p
	head := "diff --git a/" + p + " b/" + p + "\n"
	switch {
	case !aExists:
		head, from = head+"new file mode 100644\n", "/dev/null"
	case !bExists:
		head, to = head+"deleted file mode 100644\n", "/dev/null"
	}
	return head + "--- " + from + "\n+++ " + to + "\n" + h
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
	out, paths := []FileDiff{}, map[string]string{} // either side may be nil (Patch's built)
	maps.Copy(paths, a)
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
