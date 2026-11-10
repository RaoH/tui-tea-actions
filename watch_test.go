package main

import (
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCheckWatchedNotifiesOnceWhenRunFinishes(t *testing.T) {
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	t.Setenv("TMUX", "")

	m := newModel(config{Notify: notifyOSC777}, NewClient("https://example", ""))
	run := Run{ID: 7, Repo: "o/app", WorkflowID: "ci.yml", Index: 42, Status: "running", Title: "fix it", Ref: "main"}
	m.toggleWatch(run)

	m.checkWatched([]Run{run}) // still running: no notification
	run.Status = "failure"
	m.checkWatched([]Run{run})
	m.checkWatched([]Run{run}) // already unwatched: no repeat

	w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	got := string(buf[:n])

	want := "\x1b]777;notify;✗ app ci #42 failed;fix it (main)\x07"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(m.watched) != 0 {
		t.Fatalf("run still watched")
	}
}

func TestToggleWatchRejectsFinishedRun(t *testing.T) {
	m := newModel(config{Notify: notifyOff}, NewClient("https://example", ""))
	m.toggleWatch(Run{ID: 1, Status: "success"})
	if len(m.watched) != 0 || !strings.Contains(m.flash, "already passed") {
		t.Fatalf("watched=%v flash=%q", m.watched, m.flash)
	}
}

func TestNotifySequenceStripsControlChars(t *testing.T) {
	t.Setenv("TMUX", "")
	got := notifySequence(notifyOSC777, "a;b", "x\x07\x1by")
	if got != "\x1b]777;notify;a,b;x  y\x07" {
		t.Fatalf("got %q", got)
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = stdout
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestReportStatusFollowsWatchLifecycle(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newModel(config{Notify: notifyOff, Status: "on"}, NewClient("https://example", ""))
	run := Run{ID: 7, Repo: "o/app", WorkflowID: "ci.yml", Index: 42, Status: "running"}
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	got := captureStdout(t, func() { m.toggleWatch(run); m.reportStatus(); m.reportStatus() })
	if want := "\x1b]7501;state=working:app=tui-tea-actions:msg=" + enc("watching app ci #42 running") + "\x1b\\"; got != want {
		t.Fatalf("working: got %q want %q (sent once)", got, want)
	}

	run.Status = "failure"
	got = captureStdout(t, func() { m.checkWatched([]Run{run}); m.reportStatus() })
	if want := "\x1b]7501;state=error:app=tui-tea-actions:msg=" + enc("app ci #42 failed") + "\x1b\\"; got != want {
		t.Fatalf("error: got %q want %q", got, want)
	}

	got = captureStdout(t, func() { m.Update(tea.KeyMsg{Type: tea.KeyDown}) })
	if want := "\x1b]7501;state=clear\x1b\\"; got != want {
		t.Fatalf("clear: got %q want %q", got, want)
	}
}

func mkRun(id int64, user, status string, created time.Time) Run {
	r := Run{ID: id, Repo: "o/app", WorkflowID: "ci.yml", Index: id, Status: status}
	r.Created.Time = created
	r.TriggerUser = &struct {
		Login string `json:"login"`
	}{user}
	return r
}

func TestAutoWatch(t *testing.T) {
	t.Setenv("TMUX", "")
	m := newModel(config{Notify: notifyOSC777, Status: "off", Auto: "mine", User: "raoul"}, NewClient("https://example", ""))
	old := m.autoSince.Add(-time.Hour)
	later := m.autoSince.Add(time.Minute)

	// First load: only active runs by me are watched; old finished ones are not.
	m.autoWatch([]Run{
		mkRun(1, "raoul", "running", old),
		mkRun(2, "ci-bot", "running", old),
		mkRun(3, "raoul", "success", old),
	})
	if _, ok := m.watched[1]; !ok || len(m.watched) != 1 {
		t.Fatalf("first load watched %v", m.watched)
	}

	// Unwatching by hand sticks.
	m.toggleWatch(m.watched[1])
	m.autoWatch([]Run{mkRun(1, "raoul", "running", old)})
	if len(m.watched) != 0 {
		t.Fatalf("unwatched run came back: %v", m.watched)
	}

	// A new run that started and finished between refreshes is reported.
	quick := mkRun(4, "raoul", "failure", later)
	var got string
	got = captureStdout(t, func() {
		m.autoWatch([]Run{quick})
		m.checkWatched([]Run{quick})
	})
	if !strings.Contains(got, "app ci #4 failed") {
		t.Fatalf("quick run not notified: %q", got)
	}

	// Two runs finishing together give one notification.
	a, b := mkRun(5, "raoul", "running", later), mkRun(6, "raoul", "running", later)
	m.autoWatch([]Run{a, b})
	a.Status, b.Status = "success", "failure"
	got = captureStdout(t, func() { m.checkWatched([]Run{a, b}) })
	if strings.Count(got, "\x1b]777;") != 1 || !strings.Contains(got, "✗ 2 runs finished, 1 failed") {
		t.Fatalf("batched notification: %q", got)
	}
}
