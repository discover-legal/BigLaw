// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/store"
)

// childArg makes the BigLaw binary (or a test binary, through its TestMain) a file reader:
// MaybeRunChild. childTimeout bounds one extraction; childMemory caps a child's data segment on
// Linux, so two at once (ingestSlots) fit a 4 GB Pi beside Postgres. Variables for tests.
const childArg = "__biglaw-extract"

var (
	childTimeout = 2 * time.Minute
	childMemory  = uint64(1 << 30)
	maxText      = 16 << 20 // bytes of a document's text (r2 N1: the server holds a few copies while it stores one)
	childReady   bool       // MaybeRunChild ran here, so this binary is its own reader
	errNoChild   = errors.New("docs: this binary cannot read PDFs or .docx files: its main (or a test package's TestMain) must call docs.MaybeRunChild first")
)

// MaybeRunChild turns this process into a file reader and exits, when it was started as one
// (argv: childArg, the parent's pid, a deadline in ms, a memory cap in bytes): it reads a PDF or
// .docx on stdin and writes one JSON line {X: Extraction without its text, Err: refusal, Len}
// and then the text's Len raw bytes (no escaping, so the parent reads exactly that much). No parser of an uploaded
// binary format runs in the server's process: ledongthuc/pdf recurses on nested objects, and
// deep nesting is a fatal stack overflow recover cannot catch. The child bounds itself: it exits
// at its deadline and once its parent is gone, its stack is 64 MiB, and on Linux an allocation
// past the cap is a fatal out-of-memory (the parent reports the file unreadable). main, and the
// TestMain of every package whose tests ingest a PDF or .docx, call this first.
func MaybeRunChild() {
	if len(os.Args) < 2 || os.Args[1] != childArg {
		childReady = true
		return
	}
	var arg [3]uint64
	for i := range arg {
		var err error
		if arg[i], err = strconv.ParseUint(os.Args[min(i+2, len(os.Args)-1)], 10, 63); err != nil || len(os.Args) != 5 {
			os.Exit(2)
		}
	}
	time.AfterFunc(time.Duration(arg[1])*time.Millisecond+time.Second, func() { os.Exit(3) })
	go func() {
		for range time.Tick(250 * time.Millisecond) {
			if os.Getppid() != int(arg[0]) {
				os.Exit(3)
			}
		}
	}()
	if bi, ok := debug.ReadBuildInfo(); ok && !slices.Contains(bi.Settings, debug.BuildSetting{Key: "-race", Value: "true"}) {
		limitMemory(arg[2]) // not under the race detector, whose shadow memory counts too
	}
	debug.SetMaxStack(64 << 20)
	debug.SetMemoryLimit(int64(arg[2] / 2))
	data, err := io.ReadAll(io.LimitReader(os.Stdin, store.MaxFileSize+1))
	if err != nil {
		os.Exit(2)
	}
	var out childOutput
	if out.X, err = parse(data); err != nil {
		out.Err = err.Error()
	}
	text := out.X.Text
	out.X.Text, out.Len, data = "", len(text), nil
	w := bufio.NewWriter(os.Stdout)
	if json.NewEncoder(w).Encode(out) != nil || func() error { w.WriteString(text); return w.Flush() }() != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type childOutput struct {
	X   Extraction
	Err string
	Len int
}

// readChild reads a child's output through hard bounds: a header line of at most 64 KiB, then
// exactly Len ≤ maxText bytes of valid UTF-8 without NUL into one allocation, then the end.
func readChild(r io.Reader) (out childOutput, ok bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	line, err := br.ReadSlice('\n')
	if err != nil || json.Unmarshal(line, &out) != nil || out.Len < 0 || out.Len > maxText {
		return out, false
	}
	var b strings.Builder
	b.Grow(out.Len)
	if _, err := io.CopyN(&b, br, int64(out.Len)); err != nil {
		return out, false
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return out, false
	}
	out.X.Text = b.String()
	return out, utf8.ValidString(out.X.Text) && strings.IndexByte(out.X.Text, 0) < 0
}

// childCommand is the reader child for this process: no environment (it never holds the
// server's secrets; childEnv), killed with its parent on Linux (Pdeathsig) and when ctx ends.
func childCommand(ctx context.Context, exe string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, childArg, strconv.Itoa(os.Getpid()), strconv.FormatInt(childTimeout.Milliseconds(), 10),
		strconv.FormatUint(childMemory, 10))
	cmd.Env, cmd.SysProcAttr = append([]string{}, childEnv...), childAttr()
	return cmd
}

// extractInChild runs parse in a child killed at childTimeout or when ctx ends. A crash, a
// timeout or garbage output is ErrUnreadable, never text. The child is this running image
// (selfExe: /proc/self/exe on Linux), not whatever file is at its path now.
func extractInChild(ctx context.Context, data []byte) (Extraction, error) {
	exe, err := selfExe()
	if !childReady {
		return Extraction{}, errNoChild
	} else if err != nil {
		return Extraction{}, fmt.Errorf("docs: file reader: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, childTimeout)
	defer cancel()
	cmd := childCommand(cctx, exe)
	cmd.Stdin = bytes.NewReader(data)
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	out, ok := childOutput{}, false
	if err != nil && cctx.Err() == nil { // a deadline that passed before the start (a loaded machine) is the timeout below
		return Extraction{}, fmt.Errorf("docs: the file reader could not start: %w", err)
	} else if err == nil {
		if out, ok = readChild(stdout); !ok {
			_ = cmd.Process.Kill() // it may be blocked writing more than was read
		}
		err = cmd.Wait()
	}
	switch {
	case ctx.Err() != nil:
		return Extraction{}, ctx.Err()
	case cctx.Err() != nil:
		return Extraction{}, fmt.Errorf("%w: reading the file took longer than %s", ErrUnreadable, childTimeout)
	case err != nil || !ok:
		return Extraction{}, fmt.Errorf("%w: the file reader failed on this file (it may be damaged, hostile, or need more than %d MiB)", ErrUnreadable, childMemory>>20)
	case strings.HasPrefix(out.Err, ErrUnsupported.Error()):
		return Extraction{}, fmt.Errorf("%w%s", ErrUnsupported, strings.TrimPrefix(out.Err, ErrUnsupported.Error()))
	case out.Err != "":
		return Extraction{}, fmt.Errorf("%w%s", ErrUnreadable, strings.TrimPrefix(out.Err, ErrUnreadable.Error()))
	}
	return out.X, nil
}
