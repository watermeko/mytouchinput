package ui

import (
	"fmt"
	"os"
	"image"
	"image/color"
	"image/draw"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"

	xdgshell "github.com/rajveermalviya/go-wayland/wayland/stable/xdg-shell"

	"github.com/watermeko/mytouchinput/internal/layout"
)

var (
	colBG      = color.NRGBA{0x2b, 0x2b, 0x2e, 0xff}
	colBar     = color.NRGBA{0x1f, 0x1f, 0x22, 0xff}
	colKey     = color.NRGBA{0x44, 0x44, 0x48, 0xff}
	colMod     = color.NRGBA{0x35, 0x35, 0x3a, 0xff}
	colActive  = color.NRGBA{0x3d, 0x7e, 0xbd, 0xff}
	colText    = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	colTextDim = color.NRGBA{0xd0, 0xd0, 0xd8, 0xff}
	colPressed = color.NRGBA{0x6a, 0x6a, 0x74, 0xff}
)

// Run serves the keyboard until the process exits.  It starts collapsed: only
// the small toggle button is drawn, the rest of the surface is transparent and
// click-through, and tapping the button shows or hides the keys.
func (u *UI) Run() (err error) {
	defer func() {
		if err != nil {
			_ = u.ctx.Dispatch()
			_ = u.ctx.Dispatch()
			logf("run failed: %v", err)
		}
	}()
	if err := u.ensureSurface(); err != nil {
		return err
	}

	for {
		if u.dirty {
			if err := u.paint(); err != nil {
				return err
			}
			u.dirty = false
		}

		// Poll instead of chaining wl_surface.frame callbacks: the callback
		// proxies are never destroyed, and re-requesting one every iteration
		// killed the connection after a handful of frames.  Polling also keeps
		// events flowing while collapsed, so the toggle button stays tappable.
		time.Sleep(20 * time.Millisecond)
		if err := u.roundtrip(); err != nil {
			return fmt.Errorf("polling the compositor: %w", err)
		}
	}
}

func (u *UI) ensureSurface() error {
	surface, err := u.compositor.CreateSurface()
	if err != nil {
		return err
	}
	u.surface = surface

	xdgSurface, err := u.wmBase.GetXdgSurface(surface)
	if err != nil {
		return err
	}
	u.xdgSurface = xdgSurface
	xdgSurface.SetConfigureHandler(func(ev xdgshell.SurfaceConfigureEvent) {
		u.configureSerial = ev.Serial
		if err := ackConfigure(xdgSurface, ev.Serial); err != nil {
			logf("ack_configure(%d): %v", ev.Serial, err)
		} else {
			logf("acked configure serial=%d", ev.Serial)
		}
		u.configured = true
		if err := setWindowGeometry(xdgSurface, 0, 0, int32(u.width), int32(u.height)); err != nil {
			logf("set_window_geometry: %v", err)
		} else {
			logf("set window geometry %dx%d", u.width, u.height)
		}
	})

	toplevel, err := xdgSurface.GetToplevel()
	if err != nil {
		return err
	}
	u.toplevel = toplevel
	logf("ids: surface=%d xdg_surface=%d toplevel=%d shm=%d comp=%d wm=%d seat=%d",
		surface.ID(), xdgSurface.ID(), toplevel.ID(), u.shm.ID(), u.compositor.ID(), u.wmBase.ID(), u.seat.ID())
	if err := setTitle(toplevel, u.cfg.Title); err != nil {
		return err
	}
	if err := setAppID(toplevel, u.cfg.AppID); err != nil {
		return err
	}
	// No min/max size: pinning it fights the KWin window rule that docks the
	// keyboard, and attaching a buffer that disagrees with the compositor's
	// configure gets rejected.

	logf("created surface + xdg_surface + toplevel")
	if os.Getenv("MYTOUCHINPUT_NO_WAIT") == "1" {
		logf("skipping the pre-configure commit/wait")
	} else {
		if err := surface.Commit(); err != nil {
			return err
		}
		for !u.configured {
			if err := u.ctx.Dispatch(); err != nil {
				return fmt.Errorf("waiting for configure: %w", err)
			}
		}
	}

	if err := u.createBuffers(); err != nil {
		logf("createBuffers: %v", err)
		return err
	}
	logf("created %d buffers", len(u.buffers))
	// Flush and surface any error the compositor raised about the pool/buffers
	// before we try to use them.
	if err := u.roundtrip(); err != nil {
		logf("roundtrip after buffers: %v", err)
		return err
	}
	logf("buffers accepted by the compositor")

	if os.Getenv("MYTOUCHINPUT_PROBE_ATTACH") == "1" {
		probe, err := u.compositor.CreateSurface()
		if err != nil {
			return err
		}
		logf("probe surface id=%d", probe.ID())
		if err := attachBuffer(probe, u.buffers[0], 0, 0); err != nil {
			logf("probe attach: %v", err)
			return err
		}
		logf("probe attach ok")
		if err := probe.Commit(); err != nil {
			logf("probe commit: %v", err)
			return err
		}
		logf("probe commit ok")
		if err := u.roundtrip(); err != nil {
			logf("probe roundtrip: %v", err)
			return err
		}
		logf("probe roundtrip ok -- shm attach works on a bare surface")
		os.Exit(0)
	}
	u.dirty = true
	return nil
}

func (u *UI) createBuffers() error {
	// The pool has to be page-aligned or the compositor's mmap of it fails.
	size := (u.stride*u.height + 4095) &^ 4095
	for i := range u.canvases {
		fd, err := unix.MemfdCreate("mytouchinput-shm", unix.MFD_CLOEXEC)
		if err != nil {
			return fmt.Errorf("memfd: %w", err)
		}
		if err := unix.Ftruncate(fd, int64(size)); err != nil {
			unix.Close(fd)
			return err
		}
		data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			unix.Close(fd)
			return err
		}

		pool, err := createPool(u.shm, fd, int32(size))
		if err != nil {
			unix.Close(fd)
			return err
		}
		u.pools = append(u.pools, pool)
		buf, err := createBuffer(pool, 0, int32(u.width), int32(u.height),
			int32(u.stride), shmFormatARGB8888)
		if err != nil {
			return err
		}
		u.buffers[i] = buf
		u.pixels = append(u.pixels, pixelTarget{data: data, fd: fd})
	}

	button, err := u.compositor.CreateRegion()
	if err != nil {
		return err
	}
	tr := u.toggleRect()
	if err := button.Add(int32(tr.x), int32(tr.y), int32(tr.w), int32(tr.h)); err != nil {
		return err
	}
	u.buttonRegion = button

	full, err := u.compositor.CreateRegion()
	if err != nil {
		return err
	}
	if err := full.Add(0, 0, int32(u.width), int32(u.height)); err != nil {
		return err
	}
	u.inputRegion = full
	return nil
}

type pixelTarget struct {
	data []byte
	fd   int
}

// paint renders the keyboard (or a fully transparent surface) and commits it.
func (u *UI) paint() (err error) {
	// A rejected request makes the compositor close the connection; the write
	// fails before the wl_display.error is read, so drain it for the report.
	defer func() {
		if err != nil {
			_ = u.ctx.Dispatch()
			_ = u.ctx.Dispatch()
			logf("paint failed: %v", err)
		}
	}()
	idx := u.nextBuffer
	u.nextBuffer = (u.nextBuffer + 1) % len(u.buffers)

	img := image.NewRGBA(image.Rect(0, 0, u.width, u.height))
	u.boxes = u.layout()

	u.button = u.toggleRect()
	if u.expanded {
		u.drawKeyboard(img)
	} else {
		draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)
	}
	u.drawToggle(img)

	// wl_shm ARGB8888 is little-endian 0xAARRGGBB, image.RGBA is R,G,B,A.
	dst := u.pixels[idx].data
	for y := 0; y < u.height; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+u.width*4]
		out := dst[y*u.stride : y*u.stride+u.width*4]
		for x := 0; x < u.width*4; x += 4 {
			out[x+0] = src[x+2]
			out[x+1] = src[x+1]
			out[x+2] = src[x+0]
			out[x+3] = src[x+3]
		}
	}

	logf("paint: expanded=%v boxes=%d", u.expanded, len(u.boxes))
	if os.Getenv("MYTOUCHINPUT_NO_REGION") != "1" {
		// Collapsed: only the toggle button takes input, so taps fall through
		// to whatever is underneath.
		region := u.inputRegion
		if !u.expanded {
			region = u.buttonRegion
		}
		if err := u.surface.SetInputRegion(region); err != nil {
			return fmt.Errorf("set_input_region: %w", err)
		}
	}
	logf("attach buffer id=%d", u.buffers[idx].ID())
	if os.Getenv("MYTOUCHINPUT_NO_ATTACH") != "1" {
		if err := attachBuffer(u.surface, u.buffers[idx], 0, 0); err != nil {
			return err
		}
	}
	if os.Getenv("MYTOUCHINPUT_NO_DAMAGE") != "1" {
		if err := u.surface.Damage(0, 0, int32(u.width), int32(u.height)); err != nil {
			return err
		}
	}
	if os.Getenv("MYTOUCHINPUT_NO_COMMIT") == "1" {
		return nil
	}
	return u.surface.Commit()
}

// drawToggle paints the show/hide button.
func (u *UI) drawToggle(img *image.RGBA) {
	b := u.toggleRect()
	bg := colMod
	if u.expanded {
		bg = colActive
	}
	u.roundedRect(img, b.x, b.y, b.w, b.h, 12, bg)
	// A small keyboard glyph: three rows of short bars.
	glyph := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	bx, by := b.x+10, b.y+16
	for row := 0; row < 3; row++ {
		for col := 0; col < 4; col++ {
			x := bx + col*9
			y := by + row*8
			w := 6
			if row == 2 && col == 2 {
				w = 15
			}
			draw.Draw(img, image.Rect(x, y, x+w, y+4), image.NewUniform(glyph), image.Point{}, draw.Src)
		}
	}
}

func (u *UI) drawKeyboard(img *image.RGBA) {
	draw.Draw(img, img.Bounds(), image.NewUniform(colBG), image.Point{}, draw.Src)

	// header strip
	draw.Draw(img, image.Rect(0, 0, u.width, 30), image.NewUniform(colBar),
		image.Point{}, draw.Src)
	label := "EN"
	if u.symbolsIME {
		label = "CN · pinyin"
	}
	u.text(img, label, 12, 21, colTextDim)

	for _, b := range u.boxes {
		bg := colKey
		switch b.key.Act {
		case layout.Shift, layout.ShowSymbols, layout.ShowLetters, layout.ToggleIME:
			bg = colMod
		}
		if b.key.Act == layout.Shift && u.shift {
			bg = colActive
		}
		if b.key.Act == layout.ToggleIME && u.symbolsIME {
			bg = colActive
		}
		if b.key.Label == "" {
			// space bar
			bg = colMod
		}
		u.roundedRect(img, b.x, b.y, b.w, b.h, 8, bg)

		if b.key.Label != "" {
			txt := b.key.Label
			if u.shift && layout.IsLetter(b.key) {
				txt = layout.Uppercase(b.key)
			}
			u.centre(img, txt, b)
		}
	}
}

func (u *UI) text(img *image.RGBA, s string, x, y int, c color.Color) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: u.face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func (u *UI) centre(img *image.RGBA, s string, b keyBox) {
	w := font.MeasureString(u.face, s).Ceil()
	// The face baseline: approximate with the ascent so text sits centred.
	const ascent = 22
	x := b.x + (b.w-w)/2
	y := b.y + (b.h+ascent)/2 - 4
	u.text(img, s, x, y, colText)
}

func (u *UI) roundedRect(img *image.RGBA, x, y, w, h, r int, c color.Color) {
	const steps = 8
	for dy := 0; dy < h; dy++ {
		inset := 0
		if dy < r {
			inset = r - roundInset(dy, r)
		} else if dy >= h-r {
			inset = r - roundInset(h-1-dy, r)
		}
		if inset < 0 {
			inset = 0
		}
		row := image.Rect(x+inset, y+dy, x+w-inset, y+dy+1)
		draw.Draw(img, row, image.NewUniform(c), image.Point{}, draw.Src)
	}
}

// roundInset returns how much of the corner radius still applies at a distance
// d from the corner edge.
func roundInset(d, r int) int {
	if d <= 0 {
		return 0
	}
	if d > r {
		return r
	}
	// quarter circle
	dx := float64(r) - float64(d)
	return r - int(round(sqrt(float64(r*r)-dx*dx)))
}

func sqrt(v float64) float64 {
	// Newton's method; avoids pulling in math for one call site.
	x := v
	if x <= 0 {
		return 0
	}
	for i := 0; i < 16; i++ {
		x = 0.5 * (x + v/x)
	}
	return x
}

func round(v float64) float64 {
	return float64(int(v + 0.5))
}

// press handles a tap at surface coordinates.
func (u *UI) press(x, y float64) {
	b := u.button
	if x >= float64(b.x) && x <= float64(b.x+b.w) &&
		y >= float64(b.y) && y <= float64(b.y+b.h) {
		u.expanded = !u.expanded
		u.dirty = true
		logf("toggle -> expanded=%v", u.expanded)
		return
	}
	if !u.expanded {
		return
	}
	for _, b := range u.boxes {
		if x < float64(b.x) || x > float64(b.x+b.w) ||
			y < float64(b.y) || y > float64(b.y+b.h) {
			continue
		}
		u.activate(b.key)
		u.dirty = true
		return
	}
}

func (u *UI) activate(k layout.Key) {
	switch k.Act {
	case layout.Shift:
		u.shift = !u.shift
	case layout.ShowSymbols:
		u.symbols = true
	case layout.ShowLetters:
		u.symbols = false
	case layout.ToggleIME:
		u.symbolsIME = !u.symbolsIME
	}
	u.on(k, u.shift)
}

// SetIME lets main report the current input method so the header can show it.
func (u *UI) SetIME(chinese bool) {
	if u.symbolsIME != chinese {
		u.symbolsIME = chinese
		u.dirty = true
	}
}

// Redraw schedules a repaint.
func (u *UI) Redraw() { u.dirty = true }

var _ = xdgshell.WmBasePingEvent{}
var _ = unix.MFD_CLOEXEC
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ui: "+format+"\n", args...)
}
