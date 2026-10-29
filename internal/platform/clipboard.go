package platform

import "github.com/atotto/clipboard"

// CopyToClipboard puts text into the system clipboard (Windows API; on
// Linux xclip, xsel or wl-copy must be installed).
func CopyToClipboard(text string) error {
	return clipboard.WriteAll(text)
}
