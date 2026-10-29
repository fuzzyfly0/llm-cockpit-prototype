//go:build !windows

package platform

// ConsoleWindowOrigin is always 0,0 outside Windows: terminals report mouse
// positions relative to the visible screen.
func ConsoleWindowOrigin() (x, y int) { return 0, 0 }
