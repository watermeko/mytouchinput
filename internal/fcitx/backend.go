// Package fcitx talks to fcitx5 as its "virtual keyboard backend".
//
// When fcitx5 runs as the compositor's Wayland input method it knows when a text
// field gains or loses focus, and it tells the on-screen keyboard about it by
// calling ProcessVisibilityEvent on this interface.  That is the signal that
// makes the keyboard appear and disappear, instead of sitting on screen
// permanently.
//
// The bus name, object path and signatures below were read out of
// libvirtualkeyboard.so (the D-Bus Virtual Keyboard addon); every method
// returns void, and ProcessKeyEvent takes (u32, u32, u32, bool, u32).
package fcitx

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	// BusName is the name fcitx5 looks for.
	BusName = "org.fcitx.Fcitx5.VirtualKeyboardBackend"
	// ObjectPath is where it expects the backend object.
	ObjectPath = dbus.ObjectPath("/org/fcitx/virtualkeyboard/impanel")
	// Interface is the backend interface.
	Interface = "org.fcitx.Fcitx5.VirtualKeyboardBackend1"
)

// Backend implements Interface and forwards visibility changes.
type Backend struct {
	// Visibility receives true when a text field wants a keyboard and false
	// when it goes away.  It must be buffered and drained, or fcitx5's call
	// blocks on the D-Bus reply.
	Visibility chan bool
	// Keys receives hardware key events fcitx5 mirrors to the keyboard, so the
	// UI can follow along (currently only used for logging).
	Keys chan KeyEvent
}

// KeyEvent is what fcitx5 reports through ProcessKeyEvent.
type KeyEvent struct {
	Sym         uint32
	Modifiers   uint32
	State       uint32
	Released    bool
	EventSource uint32
}

// ProcessVisibilityEvent is called by fcitx5 when the keyboard should appear or
// disappear.
func (b *Backend) ProcessVisibilityEvent(visible bool) *dbus.Error {
	select {
	case b.Visibility <- visible:
	default:
	}
	return nil
}

// ProcessKeyEvent is called for key events fcitx5 wants the keyboard to see.
func (b *Backend) ProcessKeyEvent(sym, mods, state uint32, released bool, source uint32) *dbus.Error {
	select {
	case b.Keys <- KeyEvent{Sym: sym, Modifiers: mods, State: state, Released: released, EventSource: source}:
	default:
	}
	return nil
}

// NextPage and PrevPage ask the keyboard to page its own suggestion strip; this
// implementation has none, but fcitx5 expects the methods to exist.
func (b *Backend) NextPage() *dbus.Error { return nil }

// PrevPage is the counterpart of NextPage.
func (b *Backend) PrevPage() *dbus.Error { return nil }

// SelectCandidate is sent when fcitx5 picks a candidate itself.
func (b *Backend) SelectCandidate(index int32) *dbus.Error { return nil }

// SetVirtualKeyboardFunctionMode reports which function keys fcitx5 wants.
func (b *Backend) SetVirtualKeyboardFunctionMode(mode int32) *dbus.Error { return nil }

// Serve claims the bus name and exports the backend object.
func Serve(conn *dbus.Conn, b *Backend) error {
	reply, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", BusName, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s already owned (reply %d)", BusName, reply)
	}
	if err := conn.Export(b, ObjectPath, Interface); err != nil {
		return fmt.Errorf("exporting backend: %w", err)
	}
	return nil
}

// Connect attaches to the session bus.
func Connect() (*dbus.Conn, error) {
	return dbus.SessionBus()
}