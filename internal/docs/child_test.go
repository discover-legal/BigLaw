// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// loopPDF has a page tree whose Pages node is its own kid: ledongthuc/pdf walks it forever.
func loopPDF() []byte {
	return rawPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [2 0 R] /Count 1 >>")
}

// bombPDF is one page whose FlateDecode content stream inflates to mib MiB of shown text.
func bombPDF(mib int) []byte {
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	line := []byte("(" + strings.Repeat("A", 1000) + ") Tj\n")
	for n := 0; n < mib<<20; n += len(line) {
		zw.Write(line)
	}
	zw.Close()
	return rawPDF("<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", z.Len(), z.Bytes()))
}

func rawPDF(objs ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var off []int
	for i, o := range objs {
		off = append(off, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

func raceBuild() bool {
	bi, _ := debug.ReadBuildInfo()
	return bi != nil && slices.Contains(bi.Settings, debug.BuildSetting{Key: "-race", Value: "true"})
}

// runChild starts this test binary as a reader child on data and returns how it ended and its peak
// RSS in MiB. A child that outlives limit is killed, so no test leaves one behind.
func runChild(t *testing.T, cmd *exec.Cmd, data []byte, limit time.Duration) (exit int, rssMiB int64, took time.Duration) {
	t.Helper()
	cmd.Stdin = bytes.NewReader(data)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(limit, func() { cmd.Process.Kill() })
	defer timer.Stop()
	cmd.Wait()
	ru, _ := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if ru != nil {
		rssMiB = int64(ru.Maxrss) >> 10 // KiB on Linux
	}
	return cmd.ProcessState.ExitCode(), rssMiB, time.Since(start)
}

// TestChildBoundsItself (r1 reproducer R3, R4; auditor A9; prosecutor L-1): the reader child gets no
// environment and a parent-death signal; it ends at its own deadline and when its parent is gone,
// even on a PDF that loops forever; its memory is a hard cap (a text bomb dies at the cap, not
// at the kernel's OOM killer) and its stack is 64 MiB.
func TestChildBoundsItself(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := childCommand(t.Context(), exe)
	if cmd.Env == nil || len(cmd.Env) != 0 || cmd.Args[1] != childArg || cmd.Args[2] != strconv.Itoa(os.Getpid()) {
		t.Errorf("child command: env %q args %q", cmd.Env, cmd.Args)
	}
	if runtime.GOOS == "linux" && (cmd.SysProcAttr == nil || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL) {
		t.Errorf("no parent-death signal: %+v", cmd.SysProcAttr)
	}

	old := childTimeout
	childTimeout = 300 * time.Millisecond // the deadline the child is told (no context kills it here)
	own := childCommand(context.Background(), exe)
	childTimeout = old
	if exit, _, took := runChild(t, own, loopPDF(), 20*time.Second); exit != 3 || took > 5*time.Second {
		t.Errorf("a looping PDF with a 300 ms deadline: exit %d after %s, want 3 within ~1.3 s", exit, took)
	}
	orphan := exec.Command(exe, childArg, "1", "60000", strconv.FormatUint(childMemory, 10)) // pid 1 is not its parent
	if exit, _, took := runChild(t, orphan, loopPDF(), 20*time.Second); exit != 3 || took > 3*time.Second {
		t.Errorf("a child whose parent is gone: exit %d after %s, want 3 within ~250 ms", exit, took)
	}
	if exit, _, _ := runChild(t, exec.Command(exe, childArg, "1"), nil, 20*time.Second); exit != 2 {
		t.Errorf("malformed child arguments: exit %d, want 2", exit)
	}

	if raceBuild() || runtime.GOOS != "linux" {
		t.Skip("the memory cap is Linux-only and off under the race detector (make ci runs this test without -race too)")
	}
	var stderr bytes.Buffer
	deep := pdfWith(strings.Repeat("[", 3_000_000) + strings.Repeat("]", 3_000_000))
	deepCmd := childCommand(context.Background(), exe)
	deepCmd.Stderr = &stderr
	exit, rss, took := runChild(t, deepCmd, deep, time.Minute)
	t.Logf("3M-deep nesting: exit %d, peak RSS %d MiB, %s", exit, rss, took)
	if exit == 0 || rss > 200 || !strings.Contains(stderr.String(), "stack exceeds 67108864-byte limit") {
		t.Errorf("a stack-overflow PDF: exit %d, peak RSS %d MiB, want death at the 64 MiB stack limit (default 1 GiB cap): %.200s", exit, rss, stderr.String())
	}
	oldM := childMemory
	childMemory = 256 << 20
	t.Cleanup(func() { childMemory = oldM })
	exit, rss, took = runChild(t, childCommand(context.Background(), exe), bombPDF(512), time.Minute)
	t.Logf("text bomb (512 MiB of text) under a 256 MiB cap: exit %d, peak RSS %d MiB, %s", exit, rss, took)
	if exit == 0 || rss > 320 {
		t.Errorf("a 512 MiB text bomb under a 256 MiB cap: exit %d, peak RSS %d MiB", exit, rss)
	}
	if _, err := Extract(t.Context(), bombPDF(512)); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "256 MiB") {
		t.Errorf("Extract of the bomb: %v", err)
	}
}

// TestChildDiesWithParent (r1 reproducer R4, auditor A9): a server killed with SIGKILL mid-parse
// (crash, OOM kill, container restart) takes its reader child with it. A helper process (this
// test binary, re-run) extracts a looping PDF; the test kills the helper and the child must go.
func TestChildDiesWithParent(t *testing.T) {
	if os.Getenv("DOCS_TEST_HELPER_PARENT") == "1" {
		Extract(context.Background(), loopPDF()) // blocks until this process is killed
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("finds the child through /proc")
	}
	exe, _ := os.Executable()
	helper := exec.Command(exe, "-test.run=^TestChildDiesWithParent$")
	helper.Env = append(os.Environ(), "DOCS_TEST_HELPER_PARENT=1")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { helper.Process.Kill(); helper.Wait() })
	child := 0
	for deadline := time.Now().Add(20 * time.Second); child == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		child = childOf(helper.Process.Pid)
	}
	if child == 0 {
		t.Fatal("the helper started no reader child")
	}
	t.Cleanup(func() { syscall.Kill(child, syscall.SIGKILL) }) // never leave one behind, even on failure
	helper.Process.Kill()
	helper.Wait()
	for deadline := time.Now().Add(3 * time.Second); alive(child); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("reader child %d still running 3 s after its parent was killed", child)
		}
	}
}

// childOf is the pid of a reader child whose parent is ppid, or 0.
func childOf(ppid int) int {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, _ := os.ReadFile("/proc/" + e.Name() + "/stat")
		cmdline, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:])); len(f) > 1 && f[1] == strconv.Itoa(ppid) &&
			bytes.Contains(cmdline, []byte(childArg)) {
			return pid
		}
	}
	return 0
}

// alive: the process exists and is not a zombie (a container's init may never reap it).
func alive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(f) > 0 && f[0] != "Z"
}

// TestForgottenMaybeRunChildIsAnError (auditor A2): in a binary that never called MaybeRunChild
// (a test package without the TestMain), extraction is a clear error, not the test binary
// re-running its own suite as a "child", recursively.
func TestForgottenMaybeRunChildIsAnError(t *testing.T) {
	childReady = false
	_, err := Extract(t.Context(), makePDF("x"))
	_, errDocx := Extract(t.Context(), minimalDocx("x")) // a .docx is parsed in the child too (r1 R2)
	childReady = true
	if !errors.Is(err, errNoChild) || !errors.Is(errDocx, errNoChild) {
		t.Errorf("Extract without MaybeRunChild: PDF %v, .docx %v", err, errDocx)
	}
}
