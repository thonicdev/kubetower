package launch

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Windows frame takes the console's colors. Windows
// itself cannot run here, so what is pinned is everything that decides what
// Windows is handed.

func TestAColourIsHandedToWindowsAsBGR(t *testing.T) {
	for hex, want := range map[string]uint32{
		"#ff0000": 0x000000ff,
		"#00ff00": 0x0000ff00,
		"#0000ff": 0x00ff0000,
		"#161b22": 0x00221b16,
	} {
		got, err := colorRef(hex)
		if err != nil || got != want {
			t.Errorf("colorRef(%q) = %#08x, %v; want %#08x", hex, got, err, want)
		}
	}
	for _, bad := range []string{"161b22", "#161b2", "#gggggg", ""} {
		if _, err := colorRef(bad); err == nil {
			t.Errorf("colorRef(%q) accepted", bad)
		}
	}
}

// The attribute numbers are dwmapi.h's, and dark mode comes first with the
// pre-20H1 number as its fallback: a Windows 10 refuses the three colors and
// keeps a dark bar.
func TestTheFrameSetsDarkModeFirstAndTheThreeColours(t *testing.T) {
	got := frameAttributes()
	want := []frameAttribute{
		{Name: "dark mode", ID: 20, Value: 1, Fallback: 19},
		{Name: "caption color", ID: 35, Value: 0x00221b16},
		{Name: "text color", ID: 36, Value: 0x00efe9e6},
		{Name: "border color", ID: 34, Value: 0x003a2d26},
	}
	if len(got) != len(want) {
		t.Fatalf("attributes = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attribute %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWebView2PaintsThePageColourOpaque(t *testing.T) {
	if got := webView2Background(); got != "FF0D1117" {
		t.Errorf("background = %q, want FF0D1117 (AARRGGBB, opaque)", got)
	}
}

// The colors are the theme's, not a copy that drifts from it: each one the
// frame uses is the value `web/app/theme.css` gives its token.
func TestTheFrameColoursAreTheThemes(t *testing.T) {
	css, err := os.ReadFile("../../web/app/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	for token, value := range map[string]string{
		"--color-panel": frameCaption,
		"--color-fg":    frameText,
		"--color-line":  frameBorder,
		"--color-bg":    frameBackdrop,
	} {
		found := regexp.MustCompile(regexp.QuoteMeta(token) + `:\s*(#[0-9a-fA-F]{6});`).FindSubmatch(css)
		if found == nil || !strings.EqualFold(string(found[1]), value) {
			t.Errorf("%s in theme.css is %q, the frame uses %s", token, found, value)
		}
	}
}
