package launch

import (
	"fmt"
	"strconv"
	"strings"
)

// The Windows window's frame, in the console's colors.
//
// Windows' default frame is a white title bar above a #0d1117 page. The console
// is dark only (theme.css has `color-scheme: dark` and no light palette), so the
// frame is dark, as the macOS window pins Dark Aqua; this goes the day the
// console gets a light theme.
//
//   - DWMWA_USE_IMMERSIVE_DARK_MODE: the dark title bar, attribute 20 from
//     Windows 10 20H1, 19 on builds 1809 to 1909 (tried when 20 is refused).
//   - DWMWA_CAPTION_COLOR, DWMWA_TEXT_COLOR, DWMWA_BORDER_COLOR: Windows 11
//     only. The caption takes the top bar's color so the two read as one band;
//     Windows 10 refuses them with E_INVALIDARG.
//   - Not DWMWA_SYSTEMBACKDROP_TYPE: Mica or Acrylic show only where the caption
//     is left to the system, and here it is set.
//
// This half is the arithmetic and the list, testable anywhere; the Windows half
// applies it.

// The console's colors the frame takes, from `web/app/theme.css`.
const (
	frameCaption  = "#161b22" // --color-panel, the top bar
	frameText     = "#e6e9ef" // --color-fg
	frameBorder   = "#262d3a" // --color-line
	frameBackdrop = "#0d1117" // --color-bg, the page behind everything
)

// DWM window attributes (dwmapi.h).
const (
	dwmUseImmersiveDarkModeOld = 19
	dwmUseImmersiveDarkMode    = 20
	dwmBorderColor             = 34
	dwmCaptionColor            = 35
	dwmTextColor               = 36
)

// frameAttribute is one DwmSetWindowAttribute call: a DWORD value.
type frameAttribute struct {
	Name  string
	ID    uint32
	Value uint32
	// Fallback is the attribute to try when ID is refused, 0 for none.
	Fallback uint32
}

// frameAttributes is every attribute the frame sets, in order: dark mode first,
// so a Windows 10 that refuses the colors still has a dark bar.
func frameAttributes() []frameAttribute {
	return []frameAttribute{
		{Name: "dark mode", ID: dwmUseImmersiveDarkMode, Value: 1, Fallback: dwmUseImmersiveDarkModeOld},
		{Name: "caption color", ID: dwmCaptionColor, Value: mustColorRef(frameCaption)},
		{Name: "text color", ID: dwmTextColor, Value: mustColorRef(frameText)},
		{Name: "border color", ID: dwmBorderColor, Value: mustColorRef(frameBorder)},
	}
}

// colorRef is a #rrggbb color as a Win32 COLORREF, 0x00BBGGRR.
func colorRef(hex string) (uint32, error) {
	rgb, err := parseHexRGB(hex)
	if err != nil {
		return 0, err
	}
	r, g, b := rgb>>16&0xff, rgb>>8&0xff, rgb&0xff
	return b<<16 | g<<8 | r, nil
}

func mustColorRef(hex string) uint32 {
	value, err := colorRef(hex)
	if err != nil {
		panic(err)
	}
	return value
}

// webView2Background is the color WebView2 paints before the page has drawn,
// in WEBVIEW2_DEFAULT_BACKGROUND_COLOR's AARRGGBB form: the page's own
// background, so the first frame is not a white flash.
func webView2Background() string {
	rgb, err := parseHexRGB(frameBackdrop)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("FF%06X", rgb)
}

func parseHexRGB(hex string) (uint32, error) {
	digits, ok := strings.CutPrefix(hex, "#")
	if !ok || len(digits) != 6 {
		return 0, fmt.Errorf("%q is not a #rrggbb color", hex)
	}
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not a #rrggbb color: %w", hex, err)
	}
	return uint32(value), nil
}
