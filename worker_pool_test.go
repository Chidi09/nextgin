//go:build !js

package nextgin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestPoolReprobeRestoresLiveWorker(t *testing.T) {
	p := startFakePool(t, 2)
	srv := httptest.NewServer(p)
	defer srv.Close()

	w0 := p.workers[0]
	w0.mu.Lock()
	origPID := w0.cmd.Process.Pid
	w0.mu.Unlock()

	tmpSock := w0.sock + ".bak"
	if err := os.Rename(w0.sock, tmpSock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpSock) })

	for i := 0; i < 6; i++ {
		res, err := http.Get(srv.URL + "/")
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: err=%v res=%v", i, err, res)
		}
		res.Body.Close()
	}
	if w0.ready.Load() {
		t.Fatal("worker 0 should be marked not ready after dial failure")
	}

	if err := os.Rename(tmpSock, w0.sock); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !w0.ready.Load() {
		if time.Now().After(deadline) {
			t.Fatal("worker 0 did not become ready again within 2s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	w0.mu.Lock()
	curCmd := w0.cmd
	w0.mu.Unlock()
	if curCmd == nil || curCmd.Process == nil || curCmd.Process.Pid != origPID {
		t.Fatalf("worker 0 should not have restarted (orig pid %d, cur %v)", origPID, curCmd)
	}

	wantPrefix := fmt.Sprintf("pid=%d", origPID)
	seenOrig := false
	for i := 0; i < 6; i++ {
		if pidOf(t, srv.URL+"/") == wantPrefix {
			seenOrig = true
		}
	}
	if !seenOrig {
		t.Fatalf("restored worker 0 (%s) did not serve any requests", wantPrefix)
	}
}

func TestPoolReprobeKillsUnreachableWorker(t *testing.T) {
	p := startFakePool(t, 2)
	srv := httptest.NewServer(p)
	defer srv.Close()

	w0 := p.workers[0]
	w0.mu.Lock()
	origProc := w0.cmd.Process
	origPID := origProc.Pid
	w0.mu.Unlock()

	if err := os.Remove(w0.sock); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 4; i++ {
		res, err := http.Get(srv.URL + "/")
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: err=%v res=%v", i, err, res)
		}
		res.Body.Close()
	}
	if w0.ready.Load() {
		t.Fatal("worker 0 should be marked not ready after dial failure")
	}

	deadline := time.Now().Add(8 * time.Second)
	var newPID int
	for {
		w0.mu.Lock()
		cmd := w0.cmd
		w0.mu.Unlock()
		if w0.ready.Load() && cmd != nil && cmd.Process != nil && cmd.Process.Pid != origPID {
			newPID = cmd.Process.Pid
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker 0 was not killed and restarted within 8s (ready=%v)", w0.ready.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := origProc.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("original worker process %d is still alive", origPID)
	}

	wantPrefix := fmt.Sprintf("pid=%d", newPID)
	seenNew := false
	for i := 0; i < 6; i++ {
		if pidOf(t, srv.URL+"/") == wantPrefix {
			seenNew = true
		}
	}
	if !seenNew {
		t.Fatalf("restarted worker 0 (%s) did not serve any requests", wantPrefix)
	}
}
