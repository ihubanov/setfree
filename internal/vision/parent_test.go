package vision

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// envParentTestRole turns the test binary into one of the two processes the
// real launch has, so the parent pipe is tested against an actual process
// death on every platform CI runs, not a simulated one.
const envParentTestRole = "SETFREE_VISION_PARENT_TEST_ROLE"

func TestMain(m *testing.M) {
	switch os.Getenv(envParentTestRole) {
	case "parent":
		runParentRole()
	case "proxy":
		runProxyRole()
	}
	os.Exit(m.Run())
}

// runParentRole starts a proxy stand-in the way MaybeStart does, then waits to
// be killed. The sleep bounds a leak if the test never gets that far.
func runParentRole() {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envParentTestRole+"=proxy")
	cmd.Stdout = os.Stdout
	pipe, err := cmd.StdinPipe()
	if err != nil {
		os.Exit(1)
	}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	time.Sleep(time.Minute)
	runtime.KeepAlive(pipe)
	os.Exit(0)
}

// runProxyRole announces itself and stays up until the parent pipe closes.
// The deadline bounds a leak if watchParent never fires; the test fails first.
func runProxyRole() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go watchParent(os.Stdin, cancel)
	_, _ = os.Stdout.WriteString("ready\n")
	<-ctx.Done()
	os.Exit(0)
}

// A launcher killed outright can't stop the proxy, so the proxy has to notice
// on its own. Process.Kill is SIGKILL on Unix and TerminateProcess on Windows.
func TestParentPipe_ProxyLeavesWhenParentIsKilled(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()

	parent := exec.Command(os.Args[0])
	parent.Env = append(os.Environ(), envParentTestRole+"=parent")
	parent.Stdout = w
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill() })
	w.Close()

	ready := make([]byte, len("ready\n"))
	if _, err := io.ReadFull(r, ready); err != nil {
		t.Fatalf("proxy never came up: %v", err)
	}

	// Our read end hits EOF only once the proxy, the last holder of the write
	// end, has exited.
	proxyGone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(proxyGone)
	}()

	select {
	case <-proxyGone:
		t.Fatal("proxy left while its parent was still alive")
	case <-time.After(500 * time.Millisecond):
	}

	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	_ = parent.Wait()

	select {
	case <-proxyGone:
	case <-time.After(10 * time.Second):
		t.Fatal("proxy still running 10s after its parent was killed")
	}
}

// The launcher never writes to the pipe, so only its closing may trigger this.
func TestWatchParent_LeavesOnlyOnEOF(t *testing.T) {
	r, w := io.Pipe()
	left := make(chan struct{})
	go watchParent(r, func() { close(left) })

	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-left:
		t.Fatal("watchParent left on data, not EOF")
	case <-time.After(50 * time.Millisecond):
	}

	w.Close()
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("watchParent did not leave on EOF")
	}
}
