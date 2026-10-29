// mytouchinput is an on-screen keyboard for Plasma Mobile.
//
// It shows itself when fcitx5 reports that a text field wants input, types
// through /dev/uinput (via ydotoold) so the compositor's input method -- fcitx5
// -- performs the Chinese conversion, and hides itself again when the field
// goes away.  See README.md for why it is built this way.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/watermeko/mytouchinput/internal/fcitx"
	"github.com/watermeko/mytouchinput/internal/inject"
	"github.com/watermeko/mytouchinput/internal/layout"
	"github.com/watermeko/mytouchinput/internal/ui"
)

func main() {
	var (
		width  = flag.Int("width", 1440, "keyboard width in logical pixels")
		height = flag.Int("height", 338, "keyboard height in logical pixels")
		appID  = flag.String("app-id", "org.liuqin.TouchInput", "Wayland app_id for the KWin rule")
		socket = flag.String("ydotool-socket", "/run/ydotoold.socket", "ydotoold socket")
		dryRun = flag.Bool("dry-run", false, "log key events instead of injecting them")
		always = flag.Bool("always-visible", false, "ignore fcitx5 and stay visible (debugging)")
	)
	flag.Parse()

	visibility := make(chan bool, 8)
	if *always {
		visibility <- true
	} else {
		conn, err := fcitx.Connect()
		if err != nil {
			log.Fatalf("cannot reach the session bus: %v", err)
		}
		backend := &fcitx.Backend{
			Visibility: visibility,
			Keys:       make(chan fcitx.KeyEvent, 16),
		}
		if err := fcitx.Serve(conn, backend); err != nil {
			log.Fatalf("cannot become fcitx5's virtual keyboard: %v", err)
		}
		log.Printf("registered as %s", fcitx.BusName)
	}

	inj := &inject.Injector{Socket: *socket, DryRun: *dryRun}

	kb, err := ui.New(ui.Config{
		Width:  *width,
		Height: *height,
		AppID:  *appID,
		Title:  "mytouchinput",
	}, func(k layout.Key, shift bool) {
		if k.Act == layout.ToggleIME {
			switchIME()
			return
		}
		code, holdShift := layout.Codes(k, shift)
		if code == 0 {
			return
		}
		if err := inj.Tap(code, inject.Modifiers{Shift: holdShift}); err != nil {
			log.Printf("injecting key %q: %v", k.Label, err)
		}
	})
	if err != nil {
		log.Fatalf("cannot create the keyboard window: %v", err)
	}

	if err := kb.Run(visibility); err != nil {
		log.Fatalf("keyboard stopped: %v", err)
	}
}

// switchIME asks fcitx5 to move between pinyin and plain US input.  The
// activation has to come first: with no active input context `-s` is a no-op.
func switchIME() {
	run := func(args ...string) {
		cmd := exec.Command("fcitx5-remote", args...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		_ = cmd.Run()
		time.Sleep(120 * time.Millisecond)
	}
	current := currentIME()
	if current == "pinyin" {
		run("-c")
		return
	}
	run("-o")
	run("-s", "pinyin")
}

func currentIME() string {
	out, err := exec.Command("fcitx5-remote", "-n").Output()
	if err != nil {
		return ""
	}
	return string(trimSpace(out))
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\t' || b[start] == '\r') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\t' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}

var _ = fmt.Sprintf
