package ui

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	xdgshell "github.com/rajveermalviya/go-wayland/wayland/stable/xdg-shell"
)

// This file works around a bug in the go-wayland request encoders: every request
// that carries a string declares the *padded* length of the argument instead of
// len(s)+1.  libwayland validates that field, so the compositor answers
// "invalid arguments for <object>#<id>.<request>" and drops the connection.
// Measured on KWin 6.6 with wl_registry.bind and xdg_toplevel.set_title.
//
// Only the requests this program sends are re-encoded here; everything else is
// untouched.

var le = binary.LittleEndian

// writeRequest wraps payload in a Wayland request header and sends it.
func writeRequest(proxy client.Proxy, opcode uint32, payload []byte) error {
	const header = 8
	total := header + len(payload)
	buf := make([]byte, total)
	le.PutUint32(buf[0:], proxy.ID())
	le.PutUint32(buf[4:], uint32(total)<<16|opcode&0xffff)
	copy(buf[8:], payload)
	return proxy.Context().WriteMsg(buf, nil)
}

// stringArg encodes a Wayland string: length including the NUL, then the bytes,
// padded to a multiple of four.
func stringArg(s string) []byte {
	declared := len(s) + 1
	padded := (declared + 3) &^ 3
	out := make([]byte, 4+padded)
	le.PutUint32(out, uint32(declared))
	copy(out[4:], s)
	return out
}

func uintArg(v uint32) []byte {
	out := make([]byte, 4)
	le.PutUint32(out, v)
	return out
}

// bindGlobal sends wl_registry.bind (opcode 0).
func bindGlobal(registry *client.Registry, ev client.RegistryGlobalEvent,
	version uint32, proxy client.Proxy) error {

	payload := append(uintArg(ev.Name), stringArg(ev.Interface)...)
	payload = append(payload, uintArg(version)...)
	payload = append(payload, uintArg(proxy.ID())...)
	if err := writeRequest(registry, 0, payload); err != nil {
		return fmt.Errorf("binding %s: %w", ev.Interface, err)
	}
	return nil
}

// xdg_toplevel request opcodes.
const (
	toplevelSetTitle = 2
	toplevelSetAppID = 3
)

func setTitle(t *xdgshell.Toplevel, title string) error {
	return writeRequest(t, toplevelSetTitle, stringArg(title))
}

func setAppID(t *xdgshell.Toplevel, appID string) error {
	return writeRequest(t, toplevelSetAppID, stringArg(appID))
}

// attachBuffer sends wl_surface.attach (opcode 1).
func attachBuffer(surface *client.Surface, buffer *client.Buffer, x, y int32) error {
	payload := append(uintArg(buffer.ID()), uintArg(uint32(x))...)
	payload = append(payload, uintArg(uint32(y))...)
	buf := make([]byte, 0, 8+len(payload))
	total := 8 + len(payload)
	hdr := make([]byte, 8)
	le.PutUint32(hdr[0:], surface.ID())
	le.PutUint32(hdr[4:], uint32(total)<<16|1)
	buf = append(hdr, payload...)
	return surface.Context().WriteMsg(buf, nil)
}

// writeRequestOOB is writeRequest with a file descriptor attached.
func writeRequestOOB(proxy client.Proxy, opcode uint32, payload []byte, fd int) error {
	const header = 8
	total := header + len(payload)
	buf := make([]byte, total)
	le.PutUint32(buf[0:], proxy.ID())
	le.PutUint32(buf[4:], uint32(total)<<16|opcode&0xffff)
	copy(buf[8:], payload)

	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	return proxy.Context().WriteMsg(buf, oob)
}

// createPool sends wl_shm.create_pool (opcode 0) with the memfd attached.
func createPool(shm *client.Shm, fd int, size int32) (*client.ShmPool, error) {
	pool := client.NewShmPool(shm.Context())
	payload := append(uintArg(pool.ID()), uintArg(uint32(size))...)
	if err := writeRequestOOB(shm, 0, payload, fd); err != nil {
		return nil, fmt.Errorf("create_pool: %w", err)
	}
	return pool, nil
}

// createBuffer sends wl_shm_pool.create_buffer (opcode 0).
func createBuffer(pool *client.ShmPool, offset, width, height, stride int32,
	format uint32) (*client.Buffer, error) {

	buffer := client.NewBuffer(pool.Context())
	payload := uintArg(buffer.ID())
	for _, v := range []uint32{uint32(offset), uint32(width), uint32(height), uint32(stride), format} {
		payload = append(payload, uintArg(v)...)
	}
	if err := writeRequest(pool, 0, payload); err != nil {
		return nil, fmt.Errorf("create_buffer: %w", err)
	}
	return buffer, nil
}
