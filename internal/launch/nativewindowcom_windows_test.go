//go:build windows

package launch

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

// The vtables `holdTheWindow` calls through name only the slots it calls, with
// `_` padding between them, so a miscounted pad would call the wrong method of
// a COM interface - and nothing would say so before a window misbehaved on
// somebody's Windows. Each slot is checked here against where it is: against
// `go-webview2`'s own declaration of the same interface where the library has
// one, and against WebView2.idl's declaration order where it has none.
func TestTheWindowsComSlotsAreWhereWebView2PutsThem(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	libraryVtbl := func(object any) reflect.Type {
		return reflect.TypeOf(object).Field(0).Type.Elem()
	}
	webView := libraryVtbl(edge.ICoreWebView2{})
	controller := libraryVtbl(edge.ICoreWebView2Controller{})
	settings := libraryVtbl(edge.ICoreWebView2Settings{})

	for _, tc := range []struct {
		name    string
		ours    uintptr
		library reflect.Type
		field   string
		slot    uintptr // WebView2.idl's order, IUnknown's three first
	}{
		{"ICoreWebView2Controller::get_CoreWebView2", unsafe.Offsetof(coreWebView2ControllerVtbl{}.getCoreWebView2), controller, "GetCoreWebView2", 25},
		{"ICoreWebView2::get_Settings", unsafe.Offsetof(coreWebView2Vtbl{}.getSettings), webView, "GetSettings", 3},
		{"ICoreWebView2::add_NavigationStarting", unsafe.Offsetof(coreWebView2Vtbl{}.addNavigationStarting), webView, "AddNavigationStarting", 7},
		{"ICoreWebView2::add_NewWindowRequested", unsafe.Offsetof(coreWebView2Vtbl{}.addNewWindowRequested), webView, "AddNewWindowRequested", 44},
		{"ICoreWebView2Settings::get_IsWebMessageEnabled", unsafe.Offsetof(coreWebView2SettingsVtbl{}.getIsWebMessageEnabled), settings, "GetIsWebMessageEnabled", 5},
		{"ICoreWebView2Settings::put_IsWebMessageEnabled", unsafe.Offsetof(coreWebView2SettingsVtbl{}.putIsWebMessageEnabled), settings, "PutIsWebMessageEnabled", 6},
		// No declaration in the library for these: the IDL's order only.
		{"ICoreWebView2NavigationStartingEventArgs::get_Uri", unsafe.Offsetof(navigationStartingArgsVtbl{}.getURI), nil, "", 3},
		{"ICoreWebView2NavigationStartingEventArgs::put_Cancel", unsafe.Offsetof(navigationStartingArgsVtbl{}.putCancel), nil, "", 8},
		{"ICoreWebView2NewWindowRequestedEventArgs::get_Uri", unsafe.Offsetof(newWindowRequestedArgsVtbl{}.getURI), nil, "", 3},
		{"ICoreWebView2NewWindowRequestedEventArgs::put_Handled", unsafe.Offsetof(newWindowRequestedArgsVtbl{}.putHandled), nil, "", 6},
		{"an event handler's Invoke", unsafe.Offsetof(windowEventVtbl{}.invoke), nil, "", 3},
	} {
		if tc.ours != tc.slot*ptr {
			t.Errorf("%s is at slot %d here, and WebView2 declares it at slot %d", tc.name, tc.ours/ptr, tc.slot)
		}
		if tc.library == nil {
			continue
		}
		field, ok := tc.library.FieldByName(tc.field)
		if !ok {
			t.Errorf("go-webview2's %s has no %s: the library's declaration moved", tc.library, tc.field)
			continue
		}
		if field.Offset != tc.ours {
			t.Errorf("%s is at slot %d here and at slot %d in go-webview2's %s",
				tc.name, tc.ours/ptr, field.Offset/ptr, tc.library)
		}
	}
}
