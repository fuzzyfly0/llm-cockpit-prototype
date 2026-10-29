package platform

import "golang.org/x/sys/windows"

var getAsyncKeyState = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")

// ShiftPressed reports whether a Shift key is held down right now. Terminal
// input on Windows does not tell Enter and Shift+Enter apart, so the cockpit
// asks Windows directly when Enter arrives.
func ShiftPressed() bool {
	const vkShift = 0x10
	r, _, _ := getAsyncKeyState.Call(vkShift)
	return r&0x8000 != 0
}
