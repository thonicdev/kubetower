package launch

import (
	"log"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows half of windowframe.go: what cannot run anywhere else.

const webView2BackgroundEnv = "WEBVIEW2_DEFAULT_BACKGROUND_COLOR"

// paintBeforeThePage sets the color WebView2 shows before the page draws.
// The runtime reads it when the controller is created, so it is set before the
// window exists. A value already in the environment is the operator's and kept.
func paintBeforeThePage() {
	if _, set := os.LookupEnv(webView2BackgroundEnv); set {
		return
	}
	if err := os.Setenv(webView2BackgroundEnv, webView2Background()); err != nil {
		log.Printf("[kubetower] the window's first frame stays WebView2's default (%v)", err)
	}
}

var procSetWindowPos = windows.NewLazySystemDLL("user32.dll").NewProc("SetWindowPos")

const (
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
)

// colourTheFrame sets frameAttributes on the window. A refusal is not a
// failure: Windows 10 refuses the colors and an older build the newer
// dark-mode number, so each is tried on its own. One log line says what held.
func colourTheFrame(window unsafe.Pointer) {
	if window == nil {
		log.Printf("[kubetower] the window has no handle, so its frame keeps Windows' colors")
		return
	}
	hwnd := windows.HWND(uintptr(window))
	var held, refused []string
	for _, attribute := range frameAttributes() {
		value := attribute.Value
		err := windows.DwmSetWindowAttribute(hwnd, attribute.ID, unsafe.Pointer(&value), uint32(unsafe.Sizeof(value)))
		if err != nil && attribute.Fallback != 0 {
			err = windows.DwmSetWindowAttribute(hwnd, attribute.Fallback, unsafe.Pointer(&value), uint32(unsafe.Sizeof(value)))
		}
		if err != nil {
			refused = append(refused, attribute.Name)
			continue
		}
		held = append(held, attribute.Name)
	}
	// The title bar was drawn before these were set; a frame change redraws it.
	_, _, _ = procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
	note := "the window's frame takes the console's colors: " + strings.Join(held, ", ")
	if len(held) == 0 {
		note = "the window's frame keeps Windows' colors"
	}
	if len(refused) > 0 {
		note += "; refused by this Windows (expected on Windows 10 for the colors): " + strings.Join(refused, ", ")
	}
	log.Printf("[kubetower] %s", note)
}
