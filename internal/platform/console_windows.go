package platform

import "golang.org/x/sys/windows"

// ConsoleWindowOrigin returns the top-left cell of the visible console
// window inside the screen buffer. Windows reports mouse positions relative
// to the whole buffer (including scrollback), so they must be shifted by
// this origin to match the screen the cockpit draws.
func ConsoleWindowOrigin() (x, y int) {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0
	}
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(h, &info) != nil {
		return 0, 0
	}
	return int(info.Window.Left), int(info.Window.Top)
}
