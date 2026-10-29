# mytouchinput

An on-screen keyboard for the Xiaomi Pad 6 Pro running Plasma Mobile, written in
Go.  It appears when a text field wants input and disappears when it goes away,
and it feeds Chinese through fcitx5.

Status: **window layer blocked.**  `internal/layout`, `internal/inject`,
`internal/fcitx` and `internal/ui` are written and compile; the keyboard process
runs, binds every global, creates its surface, toplevel and shm buffers (all
accepted by KWin through a roundtrip), and then dies on the first
`wl_surface.attach`.  Details in "Where it stops" below.

## The arrangement that works

```
Chrome / GTK / Qt  --text-input-v3-->  KWin  -->  fcitx5   (the Wayland input
                                                    |       method: converts to
                                                    |       pinyin and draws the
                                                    |       candidate window)
                                                    |
   mytouchinput  --uinput key events-->  KWin ------+
                 <--ProcessVisibilityEvent--  fcitx5
```

Three facts decide this:

1. **Chrome cannot use an application-side input method.**  It runs with
   `--ozone-platform=wayland --enable-wayland-ime`, so it hands its input to the
   compositor over `text-input-v3` and never looks at `GTK_IM_MODULE`.  Only a
   compositor-side input method can give it Chinese.  Verified: with fcitx5 as
   `kwinrc [Wayland] InputMethod` (the `fcitx5-wayland-launcher` entry), typing
   `nihao` into Chrome's omnibox brings up `你好` candidates inside Chrome and
   commits the character.
2. **KWin does not deliver pointer or touch events to a `wl-layer-shell`
   surface** on this compositor — measured with window-local press logging,
   under both `keyboard_interactivity=none` and `on_demand`.  A plain toplevel
   does receive them.  So the keyboard has to be an ordinary window, docked by a
   KWin window rule with `acceptfocus=false` so tapping it never steals focus
   from the field the keys must reach.
3. **fcitx5 will tell us when to appear.**  Its D-Bus Virtual Keyboard addon
   calls `ProcessVisibilityEvent` on a client that claims a specific bus name,
   which is exactly the focus signal that a layer-shell keyboard cannot get.
   This is what removes the permanently docked panel.

Key delivery stays on uinput (`ydotoold`): KWin routes those key events to the
input method, which is fcitx5, so the conversion happens in the normal path and
the application never receives the raw Latin keys as well.

## fcitx5's contract (read out of `libvirtualkeyboard.so`)

| item | value |
|---|---|
| bus name | `org.fcitx.Fcitx5.VirtualKeyboardBackend` |
| object path | `/org/fcitx/virtualkeyboard/impanel` |
| interface | `org.fcitx.Fcitx5.VirtualKeyboardBackend1` |

| method | signature |
|---|---|
| `ProcessVisibilityEvent` | `b` → void |
| `ProcessKeyEvent` | `j j j b j` → void |
| `NextPage`, `PrevPage` | — → void |
| `SelectCandidate` | `i` → void |
| `SetVirtualKeyboardFunctionMode` | `i` → void |

All return void; the mangled `ObjectVTablePropertyObjectMethodAdaptorIv...`
symbols give both the argument tuples and the empty return.

## What is done

* `internal/layout` — the two key layers, evdev codes, shift handling.
* `internal/inject` — key and text injection through `ydotool`, batched so a
  shifted key is one round trip.
* `internal/fcitx` — the D-Bus backend: claims the name, exports the object with
  the exact signatures above, and forwards `ProcessVisibilityEvent` into a
  channel.

## What is left

`internal/ui` — a raw Wayland window, because a toolkit cannot unmap and remap a
window on demand and that is the whole point:

1. `client.Connect("")` (a normal client on `wayland-0`; no KWin input-method
   socket is needed, since fcitx5 is the input method).
2. Bind `wl_compositor`, `wl_shm`, `wl_seat`, `wl_output`, `xdg_wm_base` from
   the registry; create `wl_surface` → `xdg_surface` → `xdg_toplevel` with
   app_id `org.liuqin.TouchInput`.
3. Draw into a `wl_shm` buffer: key rectangles plus labels via
   `golang.org/x/image/font/opentype` with the Go regular font, so no font files
   have to be shipped.
4. `wl_pointer`/`wl_touch` events → hit test → `internal/layout` → `internal/inject`.
5. Show/hide by creating and destroying the `xdg_toplevel` — a mapped but
   transparent window would still swallow taps meant for the application.
6. KWin window rule for `org.liuqin.TouchInput`: `position=0,526`,
   `size=1440,338`, `acceptfocus=false`, `keepabove`, `noborder`,
   `skiptaskbar`, **`enabled=true`**.  A `types=` key silently kills the match.
   `keepabove` does not work from a rule on this compositor; a KWin script has to
   set it (see this repository's `packaging/`).

Notes for whoever picks it up: `ydotoold` must be running
(`/run/ydotoold.socket`), and `kwinrc [Wayland] InputMethod` must point at
`fcitx5-wayland-launcher.desktop`.

## Where it stops

The keyboard reaches the point where the compositor has accepted everything and
then `wl_surface.attach` tears the connection down:

```
ui: buffers accepted by the compositor      <- full roundtrip after create_pool/create_buffer
ui: paint: visible=true boxes=33
ui: attach buffer id=15
ui: run failed: write unix @: sendmsg: broken pipe
```

KWin's journal says only `Data too big for buffer (1048576 + 12 > 1048576)` and
`error in client communication`, which is a read-side failure rather than a
protocol error, so the offending message is not identified.

What was ruled out, each by bisecting with the `MYTOUCHINPUT_*` switches:

* Not the pool or the buffers: a roundtrip after creating them succeeds and a
  build that never attaches stays alive indefinitely.
* Not the surface size: it fails identically at 400x200 and at 1440x338.
* Not `set_input_region`, `damage` or `commit`: skipping them does not help, and
  a build that sends all three but no attach survives.
* Not the fd transfer or the pool alignment: re-encoding `create_pool` and
  `create_buffer` by hand (with `UnixRights`, and with page-aligned pools)
  changes nothing.
* The attach request itself is canonical.  Dumped on the wire for surface 11,
  buffer 15:

  ```
  0b 00 00 00  01 00 14 00  0f 00 00 00  00 00 00 00  00 00 00 00
  ```

  object=11, opcode=1, size=0x14, buffer=15, x=0, y=0.

The same library also mis-encodes every request carrying a string (it declares
the padded length instead of `len(s)+1`), which KWin rejects with "invalid
arguments for <object>#<id>.<request>".  Those are already worked around in
`internal/ui/bind.go`.  The remaining failure looks like the same class of bug --
a request the compositor cannot parse -- but it is not one this repository has
identified yet.

Next steps that would settle it: run the same attach against a reference client
(such as a minimal C or Python Wayland client) to see whether KWin accepts it,
and if it does, replace the remaining request encoders the way
`internal/ui/bind.go` already does for bind/title/app_id.


## Touch delivery, as measured

KWin will not deliver touch events to a window whose rule sets
`acceptfocus=false`; dropping that key is what made taps arrive at all:

```
ui: touch down at 75,59 surface=true
inject: key [16:1 16:0]
```

With the rest of the rule kept (`position`, `size`, `noborder`, `keepabove`,
`skiptaskbar`) the window still docks at `0,526 1440x338` and taps land.

The catch: touch only keeps arriving while the keyboard is the active window,
and being active takes keyboard focus away from the text field the injected keys
must reach.  `kwin-script/mytouchinput-dock` reactivates the previous window the
moment the keyboard is activated (verified in the journal: "BOUNCE focus back to
google-chrome"), but that makes the keyboard inactive again and the *next* tap is
lost -- a sequence of taps delivers only the first one.

So the remaining problem is the same one from the other direction: a window that
is touch-receiving *and* never focused.  `acceptfocus=false` gives the second
without the first, and activation gives the first without the second.


## The missing piece: set_window_geometry

KWin will not deliver touch events to a window whose rule sets
`acceptfocus=false` -- unless the surface has announced its window geometry.
GTK does that automatically, which is why the GTK prototype worked and this one
did not.  `xdg_surface.set_window_geometry` had to be sent after the configure;
after that both properties hold at once:

* the rule keeps `acceptfocus=false`, so tapping never takes focus from the
  field the keys must reach;
* `ui: touch down` is logged for every tap, in sequence.

Verified on the device: tapping `a` and `h` typed `ah` into a search field;
tapping 中/英 moved fcitx5 to pinyin; tapping `n u h a o` brought up the
candidate strip (`1. 怒号 2. 怒 ...`).

## Showing and hiding

The keyboard does not react to focus.  It starts collapsed, showing only a
56x56 button at the bottom right with a keyboard glyph; tapping it expands the
keys, tapping it again collapses them.

Because the surface is always mapped, "collapsed" means: the rest of the surface
is painted fully transparent and the input region is narrowed to the button
alone, so taps outside it fall through to whatever is underneath.  That is why
the toggle works without the window ever taking focus.

The fcitx5 backend is still registered so fcitx5 recognises the keyboard, but
`ProcessVisibilityEvent` is ignored -- fcitx5 never called it anyway.

## Known rough edges

* Glyphs: the embedded Go font has no ⇧ ⌫ ⏎, which rendered as empty boxes.
  The keyboard now loads DejaVuSans from the system when present, so the
  modifier keys read properly, and the labels avoid Han characters.
* `noborder` from the window rule did not take effect in the last runs, so the
  window still shows a title bar.
* Show/hide: fcitx5 does notice the keyboard -- its journal prints
  "VirtualKeyboard new owner: :1.287" once we claim
  `org.fcitx.Fcitx5.VirtualKeyboard` as well as the backend name -- but it never
  calls ProcessVisibilityEvent when a text field gains focus, so the keyboard
  stays hidden.  Testing therefore uses --always-visible.
* Autostart is not wired up; the GTK prototype is still the active keyboard on
  the device.
