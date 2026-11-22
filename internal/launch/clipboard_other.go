//go:build !windows

package launch

// toClipboard has no clipboard to reach off Windows, and returns false.
//
// Reaching one there means shelling out to pbcopy, wl-copy or xclip and
// guessing wrong on a headless box. On Windows it is needed because neither a
// message box nor a console without QuickEdit can be selected; a terminal on
// macOS or Linux selects the banner normally. The caller claims the password is
// on the clipboard only when this says so.
func toClipboard(string) bool { return false }
