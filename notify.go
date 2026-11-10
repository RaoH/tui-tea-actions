package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// Desktop notifications use the same escape sequences Claude Code uses. Rex,
// iTerm2, Ghostty, WezTerm, kitty and others turn them into a system notification.
const (
	notifyOSC777 = "osc777" // ESC ] 777;notify;title;body BEL (title + body)
	notifyOSC9   = "osc9"   // ESC ] 9;body BEL (body only)
	notifyBell   = "bell"
	notifyOff    = "off"
)

func validNotifyMode(m string) bool {
	switch m {
	case notifyOSC777, notifyOSC9, notifyBell, notifyOff:
		return true
	}
	return false
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func notifySequence(mode, title, body string) string {
	title, body = clean(title), clean(body)
	var seq string
	switch mode {
	case notifyOSC777:
		seq = fmt.Sprintf("\x1b]777;notify;%s;%s\x07", strings.ReplaceAll(title, ";", ","), body)
	case notifyOSC9:
		seq = fmt.Sprintf("\x1b]9;%s: %s\x07", title, body)
	case notifyBell:
		seq = "\x07"
	default:
		return ""
	}
	// tmux swallows OSC sequences unless they are wrapped for passthrough.
	if os.Getenv("TMUX") != "" && mode != notifyBell {
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

// sendNotification writes the sequence in a single write so it can't land in
// the middle of a frame from Bubble Tea's renderer.
func sendNotification(mode, title, body string) {
	if seq := notifySequence(mode, title, body); seq != "" {
		_, _ = os.Stdout.Write([]byte(seq))
	}
}

// Program status (OSC 7501) is how Claude Code shows working/done/blocked on a
// Rex tab. Fields are colon-separated; title and msg are base64 encoded.
//
//	ESC ] 7501 ; state=working:app=tui-tea-actions:msg=<base64> ESC \
//	ESC ] 7501 ; state=clear ESC \
const (
	statusWorking = "working"
	statusDone    = "done"
	statusError   = "error"
	statusClear   = "clear"
)

func programStatusSequence(state, msg string) string {
	body := "state=" + state
	if state != statusClear {
		body += ":app=tui-tea-actions"
		if msg != "" {
			body += ":msg=" + base64.StdEncoding.EncodeToString([]byte(clean(msg)))
		}
	}
	seq := "\x1b]7501;" + body + "\x1b\\"
	if os.Getenv("TMUX") != "" {
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

// statusSupported reports whether to send OSC 7501. Rex sets REX_BLOCK in its
// terminals; other terminals would just ignore the sequence, but there's no
// point sending it.
func statusSupported(mode string) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return os.Getenv("REX_BLOCK") != ""
}

func sendProgramStatus(state, msg string) {
	_, _ = os.Stdout.Write([]byte(programStatusSequence(state, msg)))
}
