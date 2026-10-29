// Package ui implements the keyboard window as a plain Wayland client.
//
// It is deliberately not built on a GUI toolkit: the window has to switch
// between two states on demand, and a toolkit has no way to make a mapped
// window both invisible and click-through.  Instead the surface is always
// mapped, and when no text field wants input it is painted fully transparent
// with an empty input region -- no pixels, no swallowed taps.
//
// Why a plain toplevel at all: KWin 6.6 on this device delivers no pointer or
// touch events to a wl-layer-shell surface (measured with window-local press
// logging, under both keyboard_interactivity none and on_demand), while an
// ordinary window does.  A KWin rule docks it and sets acceptfocus=false so
// tapping a key never takes focus away from the field the keys must reach.
package ui

import (
	"fmt"
	"os"
	"image"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	xdgshell "github.com/rajveermalviya/go-wayland/wayland/stable/xdg-shell"

	"github.com/watermeko/mytouchinput/internal/layout"
)

// shmFormatARGB8888 is WL_SHM_FORMAT_ARGB8888.
const shmFormatARGB8888 = 0

// Config describes the keyboard surface.
type Config struct {
	Width  int    // logical pixels, must match the KWin rule's size
	Height int    // logical pixels
	AppID  string // matched by the KWin rule
	Title  string
}

// Handler receives a key press from the user.
type Handler func(k layout.Key, shift bool)

type keyBox struct {
	key layout.Key
	x   int
	y   int
	w   int
	h   int
}

// UI owns the Wayland connection and the keyboard surface.
type UI struct {
	cfg Config
	on  Handler

	display    *client.Display
	ctx        *client.Context
	registry   *client.Registry
	compositor *client.Compositor
	shm        *client.Shm
	seat       *client.Seat
	pointer    *client.Pointer
	touch      *client.Touch
	wmBase     *xdgshell.WmBase
	surface    *client.Surface
	xdgSurface *xdgshell.Surface
	toplevel   *xdgshell.Toplevel

	width, height int
	stride        int
	canvases      [2]*image.RGBA
	buffers       [2]*client.Buffer
	nextBuffer    int
	configured    bool
	inputRegion   *client.Region
	emptyRegion   *client.Region

	visible bool
	dirty   bool
	shift   bool
	symbols bool
	pools   []*client.ShmPool
	pixels  []pixelTarget
	lastX   float64
	lastY   float64
	boxes   []keyBox
	pressed int
	symbolsIME bool

	face font.Face
}

// New connects to the compositor and creates the (initially hidden) keyboard.
func New(cfg Config, on Handler) (*UI, error) {
	u := &UI{cfg: cfg, on: on, width: cfg.Width, height: cfg.Height,
		stride: cfg.Width * 4, pressed: -1}

	face, err := newFace(22)
	if err != nil {
		return nil, err
	}
	u.face = face

	display, err := client.Connect("")
	if err != nil {
		return nil, fmt.Errorf("connecting to the compositor: %w", err)
	}
	u.display = display
	display.SetErrorHandler(func(ev client.DisplayErrorEvent) {
		fmt.Fprintf(os.Stderr, "compositor error: object=%v code=%d message=%q\n",
			ev.ObjectId, ev.Code, ev.Message)
	})
	u.ctx = display.Context()
	logf("connected")

	registry, err := display.GetRegistry()
	if err != nil {
		return nil, err
	}
	u.registry = registry
	registry.SetGlobalHandler(u.global)
	logf("registry")

	if err := u.roundtrip(); err != nil {
		return nil, fmt.Errorf("collecting globals: %w", err)
	}
	logf("roundtrip done: comp=%v shm=%v wm=%v seat=%v", u.compositor != nil, u.shm != nil, u.wmBase != nil, u.seat != nil)
	if u.compositor == nil || u.shm == nil || u.wmBase == nil {
		return nil, fmt.Errorf("missing globals: compositor=%v shm=%v xdg_wm_base=%v",
			u.compositor != nil, u.shm != nil, u.wmBase != nil)
	}
	if u.seat == nil {
		return nil, fmt.Errorf("no wl_seat on this compositor")
	}

	// A second roundtrip flushes any error the compositor raised while we were
	// binding: those arrive after the first done, so the connection can look
	// healthy while it is already closed.
	if err := u.roundtrip(); err != nil {
		return nil, fmt.Errorf("connection died while binding globals: %w", err)
	}
	logf("second roundtrip ok")

	return u, nil
}

func (u *UI) global(ev client.RegistryGlobalEvent) {
	logf("global %q name=%d version=%d", ev.Interface, ev.Name, ev.Version)
	switch ev.Interface {
	case "wl_compositor":
		u.compositor = client.NewCompositor(u.ctx)
		logf("bind wl_compositor v4 id=%d", u.compositor.ID())
		if err := bindGlobal(u.registry, ev, 4, u.compositor); err != nil {
			logf("bind wl_compositor: %v", err)
		}
	case "wl_shm":
		u.shm = client.NewShm(u.ctx)
		logf("bind wl_shm v1 id=%d", u.shm.ID())
		if err := bindGlobal(u.registry, ev, 1, u.shm); err != nil {
			logf("bind wl_shm: %v", err)
		}
	case "wl_seat":
		u.seat = client.NewSeat(u.ctx)
		logf("bind wl_seat v5 id=%d", u.seat.ID())
		if err := bindGlobal(u.registry, ev, 5, u.seat); err != nil {
			logf("bind wl_seat: %v", err)
		}
		u.seat.SetCapabilitiesHandler(u.capabilities)
	case "xdg_wm_base":
		u.wmBase = xdgshell.NewWmBase(u.ctx)
		logf("bind xdg_wm_base v2 id=%d", u.wmBase.ID())
		if err := bindGlobal(u.registry, ev, 2, u.wmBase); err != nil {
			logf("bind xdg_wm_base: %v", err)
		}
		u.wmBase.SetPingHandler(func(ev xdgshell.WmBasePingEvent) {
			u.wmBase.Pong(ev.Serial)
		})
	}
}

func (u *UI) capabilities(ev client.SeatCapabilitiesEvent) {
	const (
		pointer = 1
		touch   = 4
	)
	if ev.Capabilities&pointer != 0 && u.pointer == nil {
		p, err := u.seat.GetPointer()
		if err != nil {
			return
		}
		u.pointer = p
		p.SetEnterHandler(func(e client.PointerEnterEvent) {
			u.lastX, u.lastY = e.SurfaceX, e.SurfaceY
		})
		p.SetMotionHandler(func(e client.PointerMotionEvent) {
			u.lastX, u.lastY = e.SurfaceX, e.SurfaceY
		})
		p.SetButtonHandler(func(e client.PointerButtonEvent) {
			// BTN_LEFT is 0x110; state 1 is pressed.
			if e.Button != 0x110 || e.State != 1 {
				return
			}
			u.press(u.lastX, u.lastY)
		})
	}
	if ev.Capabilities&touch != 0 && u.touch == nil {
		t, err := u.seat.GetTouch()
		if err != nil {
			return
		}
		u.touch = t
		t.SetDownHandler(func(e client.TouchDownEvent) {
			if e.Surface == nil {
				return
			}
			u.press(e.X, e.Y)
		})
	}
}

func newFace(size float64) (font.Face, error) {
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

// layout computes the key rectangles for the current layer.
func (u *UI) layout() []keyBox {
	rows := layout.Qwerty
	if u.symbols {
		rows = layout.Symbols
	}

	const gap = 6
	margin := 6
	top := 30 // header strip
	rowH := 54

	boxes := make([]keyBox, 0, 40)
	y := top
	for _, row := range rows {
		total := 0
		for _, k := range row {
			w := k.Width
			if w == 0 {
				w = 1
			}
			total += w
		}
		avail := u.width - 2*margin - gap*(len(row)-1)
		unit := float64(avail) / float64(total)
		x := margin
		for _, k := range row {
			w := k.Width
			if w == 0 {
				w = 1
			}
			kw := int(float64(w)*unit)
			boxes = append(boxes, keyBox{key: k, x: x, y: y, w: kw, h: rowH})
			x += kw + gap
		}
		y += rowH + gap
	}
	return boxes
}
// roundtrip waits until the compositor has delivered every event queued so far,
// which is how you know the whole globals list has arrived.
func (u *UI) roundtrip() error {
	callback, err := u.display.Sync()
	if err != nil {
		// The compositor closes the connection after sending wl_display.error,
		// and the write fails before we ever read it.  Drain whatever is still
		// queued so the error handler can report the offending request.
		_ = u.ctx.Dispatch()
		_ = u.ctx.Dispatch()
		return err
	}
	done := make(chan struct{}, 1)
	callback.SetDoneHandler(func(client.CallbackDoneEvent) {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	for {
		if err := u.ctx.Dispatch(); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
}
