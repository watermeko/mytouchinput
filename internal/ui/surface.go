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

// Run serves the keyboard until the process exits.  visibility tells it whether
// a text field wants input.
func (u *UI) Run(visibility <-chan bool) (err error) {
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
		drained := false
	drain:
		for {
			select {
			case v := <-visibility:
				if v != u.visible {
					u.visible = v
					u.dirty = true
				}
				drained = true
				continue
			default:
				break drain
			}
		}
		_ = drained

		if u.dirty {
			if os.Getenv("MYTOUCHINPUT_NO_DRAW") != "1" {
				if err := u.paint(); err != nil {
					return err
				}
			}
			u.dirty = false
		}

		if !u.visible {
			// Nothing to do; sleeping also lets the visibility channel be
			// checked again without blocking on the compositor.
			time.Sleep(40 * time.Millisecond)
			continue
		}

		// Keep a frame callback pending so Dispatch returns and the loop can
		// notice visibility changes.
		if os.Getenv("MYTOUCHINPUT_NO_DRAW") == "1" {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if _, err := u.surface.Frame(); err != nil {
			return err
		}
		if err := u.surface.Commit(); err != nil {
			return err
		}
		if err := u.ctx.Dispatch(); err != nil {
			return fmt.Errorf("dispatching: %w", err)
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
		xdgSurface.AckConfigure(ev.Serial)
		u.configured = true
	})

	toplevel, err := xdgSurface.GetToplevel()
	if err != nil {
		return err
	}
	u.toplevel = toplevel
	if err := setTitle(toplevel, u.cfg.Title); err != nil {
		return err
	}
	if err := setAppID(toplevel, u.cfg.AppID); err != nil {
		return err
	}
	toplevel.SetMinSize(int32(u.width), int32(u.height))
	toplevel.SetMaxSize(int32(u.width), int32(u.height))

	logf("created surface + xdg_surface + toplevel")
	if err := surface.Commit(); err != nil {
		return err
	}
	// Wait for the initial configure before attaching a buffer.
	for !u.configured {
		if err := u.ctx.Dispatch(); err != nil {
			return fmt.Errorf("waiting for configure: %w", err)
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

	empty, err := u.compositor.CreateRegion()
	if err != nil {
		return err
	}
	u.emptyRegion = empty

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

	if u.visible {
		u.drawKeyboard(img)
	} else {
		draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)
	}

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

	logf("paint: visible=%v boxes=%d", u.visible, len(u.boxes))
	if os.Getenv("MYTOUCHINPUT_NO_REGION") != "1" {
	if u.visible {
		if err := u.surface.SetInputRegion(u.inputRegion); err != nil {
			return fmt.Errorf("set_input_region: %w", err)
		}
	} else {
		if err := u.surface.SetInputRegion(u.emptyRegion); err != nil {
			return fmt.Errorf("set_input_region: %w", err)
		}
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

func (u *UI) drawKeyboard(img *image.RGBA) {
	draw.Draw(img, img.Bounds(), image.NewUniform(colBG), image.Point{}, draw.Src)

	// header strip
	draw.Draw(img, image.Rect(0, 0, u.width, 30), image.NewUniform(colBar),
		image.Point{}, draw.Src)
	label := "EN"
	if u.symbolsIME {
		label = "中文 · pinyin"
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
	if !u.visible {
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
