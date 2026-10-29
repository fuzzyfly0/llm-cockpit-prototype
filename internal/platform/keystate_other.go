//go:build !windows

package platform

// ShiftPressed cannot be detected outside Windows; alt+enter, ctrl+j or a
// trailing backslash insert a new line there.
func ShiftPressed() bool { return false }
