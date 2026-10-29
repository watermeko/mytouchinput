// Package inject turns key presses into real input events.
//
// The keys go through /dev/uinput (via ydotool), so whatever the compositor's
// input method is sees them exactly like a physical keyboard.  That matters
// here: KWin forwards key events to fcitx5's Wayland input method, which is
// what performs the pinyin conversion.  Committing text directly from this
// program would bypass it.
package inject

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	shift = 42
	ctrl  = 29
	alt   = 56
)

// Injector emits key events through ydotool.
type Injector struct {
	// Socket is the ydotoold socket; empty means the tool's default.
	Socket string
	// DryRun only logs what would be sent.
	DryRun bool
}

// Modifiers describes which modifier keys to hold around a tap.
type Modifiers struct {
	Shift bool
	Ctrl  bool
	Alt   bool
}

func (i *Injector) env() []string {
	env := os.Environ()
	if i.Socket != "" {
		env = append(env, "YDOTOOL_SOCKET="+i.Socket)
	}
	return env
}

// Tap presses and releases code, wrapping it in the requested modifiers.
func (i *Injector) Tap(code uint16, mods Modifiers) error {
	var events []string
	if mods.Ctrl {
		events = append(events, fmt.Sprintf("%d:1", ctrl))
	}
	if mods.Alt {
		events = append(events, fmt.Sprintf("%d:1", alt))
	}
	if mods.Shift {
		events = append(events, fmt.Sprintf("%d:1", shift))
	}
	events = append(events,
		fmt.Sprintf("%d:1", code),
		fmt.Sprintf("%d:0", code),
	)
	if mods.Shift {
		events = append(events, fmt.Sprintf("%d:0", shift))
	}
	if mods.Alt {
		events = append(events, fmt.Sprintf("%d:0", alt))
	}
	if mods.Ctrl {
		events = append(events, fmt.Sprintf("%d:0", ctrl))
	}

	if i.DryRun {
		fmt.Printf("inject: key %v\n", events)
		return nil
	}

	args := append([]string{"key"}, events...)
	cmd := exec.Command("ydotool", args...)
	cmd.Env = i.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ydotool key: %w (%s)", err, out)
	}
	return nil
}

// TypeText types a UTF-8 string, used for symbols that have no single evdev
// code.
func (i *Injector) TypeText(text string) error {
	if i.DryRun {
		fmt.Printf("inject: text %q\n", text)
		return nil
	}
	cmd := exec.Command("ydotool", "type", "--", text)
	cmd.Env = i.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ydotool type: %w (%s)", err, out)
	}
	return nil
}