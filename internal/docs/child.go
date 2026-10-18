// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
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

	"github.com/discover-legal/BigLaw/internal/store"
)

// childArg makes the BigLaw binary (or a test binary, through its TestMain) a file reader:
// MaybeRunChild. childTimeout bounds one extraction; childMemory caps a child's data segment on
// Linux, so two at once (ingestSlots) fit a 4 GB Pi beside Postgres. Variables for tests.
const childArg = "__biglaw-extract"

var (
	childTimeout = 2 * time.Minute
	childMemory  = uint64(1 << 30)
	maxText      = store.MaxFileSize // a PDF's extracted text
	childReady   bool                // MaybeRunChild ran here, so this binary is its own reader
	errNoChild   = errors.New("docs: this binary cannot read PDFs or .docx files: its main (or a test package's TestMain) must call docs.MaybeRunChild first")
)

// MaybeRunChild turns this process into a file reader and exits, when it was started as one
// (argv: childArg, the parent's pid, a deadline in ms, a memory cap in bytes): it reads a PDF or
// .docx on stdin and writes {X: Extraction, Err: refusal} as JSON. No parser of an uploaded
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
		if arg[i], err = strconv.ParseUint(os.Args[min(i+2, len(os.Args)-1)], 10, 64); err != nil || len(os.Args) != 5 {
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
	var out struct {
		X   Extraction
		Err string
	}
	if out.X, err = parse(data); err != nil {
		out.Err = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if enc.Encode(out) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// childCommand is the reader child for this process: no environment (it never holds the
// server's secrets), killed with its parent on Linux (Pdeathsig) and when ctx ends.
func childCommand(ctx context.Context, exe string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, childArg, strconv.Itoa(os.Getpid()), strconv.FormatInt(childTimeout.Milliseconds(), 10),
		strconv.FormatUint(childMemory, 10))
	cmd.Env, cmd.SysProcAttr = []string{}, childAttr()
	return cmd
}

// extractInChild runs parse in a child killed at childTimeout or when ctx ends. A crash, a
// timeout or garbage output is ErrUnreadable, never text.
func extractInChild(ctx context.Context, data []byte) (Extraction, error) {
	exe, err := os.Executable()
	if !childReady {
		return Extraction{}, errNoChild
	} else if err != nil {
		return Extraction{}, fmt.Errorf("docs: file reader: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, childTimeout)
	defer cancel()
	cmd := childCommand(cctx, exe)
	var stdout bytes.Buffer
	cmd.Stdin, cmd.Stdout = bytes.NewReader(data), &stdout
	err = cmd.Run()
	var out struct {
		X   Extraction
		Err string
	}
	switch {
	case ctx.Err() != nil:
		return Extraction{}, ctx.Err()
	case cctx.Err() != nil:
		return Extraction{}, fmt.Errorf("%w: reading the file took longer than %s", ErrUnreadable, childTimeout)
	case err != nil || json.Unmarshal(stdout.Bytes(), &out) != nil:
		return Extraction{}, fmt.Errorf("%w: the file reader failed on this file (it may be damaged, hostile, or need more than %d MiB)", ErrUnreadable, childMemory>>20)
	case strings.HasPrefix(out.Err, ErrUnsupported.Error()):
		return Extraction{}, fmt.Errorf("%w%s", ErrUnsupported, strings.TrimPrefix(out.Err, ErrUnsupported.Error()))
	case out.Err != "":
		return Extraction{}, fmt.Errorf("%w%s", ErrUnreadable, strings.TrimPrefix(out.Err, ErrUnreadable.Error()))
	}
	return out.X, nil
}
