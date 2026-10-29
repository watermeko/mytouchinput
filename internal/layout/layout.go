// Package layout defines the on-screen keyboard's keys.
//
// Each key carries the Linux evdev code that gets injected, so the application
// (or fcitx5, when it is the compositor's input method) sees a real key event.
package layout

// Action distinguishes keys that emit an evdev code from keys that change the
// keyboard's own state or talk to the input method.
type Action int

const (
	// Emit injects Code, optionally with Shift held.
	Emit Action = iota
	// Shift toggles the shift state.
	Shift
	// Symbols switches to the ?123 layer.
	ShowSymbols
	// Letters switches back to the qwerty layer.
	ShowLetters
	// ToggleIME asks fcitx5 to switch between pinyin and plain US input.
	ToggleIME
)

// Key is one button on the keyboard.
type Key struct {
	Label string
	Code  uint16
	Shift bool // Code needs Shift held to produce Label's glyph
	Act   Action
	Width int // relative width, default 1
}

const (
	shiftCode = 42
	ctrlCode  = 29
)

// Row is a horizontal line of keys.
type Row []Key

// Qwerty is the default letter layer.
var Qwerty = []Row{
	{
		{Label: "q", Code: 16}, {Label: "w", Code: 17}, {Label: "e", Code: 18},
		{Label: "r", Code: 19}, {Label: "t", Code: 20}, {Label: "y", Code: 21},
		{Label: "u", Code: 22}, {Label: "i", Code: 23}, {Label: "o", Code: 24},
		{Label: "p", Code: 25},
	},
	{
		{Label: "a", Code: 30}, {Label: "s", Code: 31}, {Label: "d", Code: 32},
		{Label: "f", Code: 33}, {Label: "g", Code: 34}, {Label: "h", Code: 35},
		{Label: "j", Code: 36}, {Label: "k", Code: 37}, {Label: "l", Code: 38},
	},
	{
		{Label: "⇧", Act: Shift, Width: 2},
		{Label: "z", Code: 44}, {Label: "x", Code: 45}, {Label: "c", Code: 46},
		{Label: "v", Code: 47}, {Label: "b", Code: 48}, {Label: "n", Code: 49},
		{Label: "m", Code: 50},
		{Label: "⌫", Code: 14, Width: 2},
	},
	{
		{Label: "?123", Act: ShowSymbols, Width: 2},
		{Label: "中/英", Act: ToggleIME, Width: 3},
		{Label: "", Code: 57, Width: 7},
		{Label: ".", Code: 52, Width: 2},
		{Label: "⏎", Code: 28, Width: 2},
	},
}

// Symbols is the ?123 layer.
var Symbols = []Row{
	{
		{Label: "1", Code: 2}, {Label: "2", Code: 3}, {Label: "3", Code: 4},
		{Label: "4", Code: 5}, {Label: "5", Code: 6}, {Label: "6", Code: 7},
		{Label: "7", Code: 8}, {Label: "8", Code: 9}, {Label: "9", Code: 10},
		{Label: "0", Code: 11},
	},
	{
		{Label: "-", Code: 12}, {Label: "/", Code: 53}, {Label: ":", Code: 39, Shift: true},
		{Label: ";", Code: 39}, {Label: "(", Code: 9, Shift: true},
		{Label: ")", Code: 10, Shift: true}, {Label: "$", Code: 5, Shift: true},
		{Label: "&", Code: 8, Shift: true}, {Label: "@", Code: 3, Shift: true},
		{Label: "\"", Code: 40, Shift: true},
	},
	{
		{Label: "⇧", Act: Shift, Width: 2},
		{Label: ".", Code: 52}, {Label: ",", Code: 51},
		{Label: "?", Code: 53, Shift: true}, {Label: "!", Code: 2, Shift: true},
		{Label: "'", Code: 40}, {Label: "~", Code: 41, Shift: true},
		{Label: "_", Code: 12, Shift: true},
		{Label: "⌫", Code: 14, Width: 2},
	},
	{
		{Label: "abc", Act: ShowLetters, Width: 2},
		{Label: "中/英", Act: ToggleIME, Width: 3},
		{Label: "", Code: 57, Width: 7},
		{Label: "+", Code: 13, Shift: true, Width: 2},
		{Label: "⏎", Code: 28, Width: 2},
	},
}

// Uppercase returns the label to show for a letter key while Shift is active.
func Uppercase(k Key) string {
	if k.Code == 0 || k.Act != Emit {
		return k.Label
	}
	if len(k.Label) == 1 && k.Label[0] >= 'a' && k.Label[0] <= 'z' {
		return string(k.Label[0] - 'a' + 'A')
	}
	return k.Label
}

// Codes returns the evdev codes to inject for a key press, given the shift
// state.  The second return value reports whether Shift has to be held.
func Codes(k Key, shifted bool) (code uint16, holdShift bool) {
	if k.Code == 0 {
		return 0, false
	}
	if k.Shift {
		// The glyph lives on the shifted level; pressing it means the user
		// wants that glyph regardless of the current shift state.
		return k.Code, !shifted
	}
	if len(k.Label) == 1 && k.Label[0] >= 'A' && k.Label[0] <= 'Z' {
		return k.Code, !shifted
	}
	return k.Code, shifted
}

// IsLetter reports whether the key is a plain letter that Shift capitalises.
func IsLetter(k Key) bool {
	return k.Act == Emit && len(k.Label) == 1 &&
		((k.Label[0] >= 'a' && k.Label[0] <= 'z') || (k.Label[0] >= 'A' && k.Label[0] <= 'Z'))
}