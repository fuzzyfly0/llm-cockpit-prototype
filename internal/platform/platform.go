// Package platform wraps OS- and git-specific actions for Windows and Linux.
package platform

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// OpenFolder opens a directory in the system file manager.
func OpenFolder(dir string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// OpenURL opens a URL in the default browser.
func OpenURL(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// OpenIDE runs the configured IDE launcher (e.g. "idea", "code") on dir.
func OpenIDE(ide, dir string) error {
	if ide == "" {
		return errors.New("keine IDE konfiguriert")
	}
	bin, err := exec.LookPath(ide)
	if err != nil && (ide == "idea" || ide == "idea64") {
		bin = FindIntelliJ()
	}
	if bin == "" {
		return fmt.Errorf("IDE %q nicht gefunden (config.ide in state.json setzen)", ide)
	}
	cmd := exec.Command(bin, dir)
	cmd.Dir = dir
	return cmd.Start()
}

// FindIntelliJ looks for an IntelliJ IDEA launcher in the usual install
// locations (standalone installer and JetBrains Toolbox) and returns the
// newest one, or "" if none is found.
func FindIntelliJ() string {
	var patterns []string
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		patterns = []string{
			filepath.Join(os.Getenv("ProgramFiles"), "JetBrains", "IntelliJ IDEA*", "bin", "idea64.exe"),
			filepath.Join(local, "Programs", "IntelliJ IDEA*", "bin", "idea64.exe"),
			filepath.Join(local, "JetBrains", "Toolbox", "scripts", "idea*.cmd"),
		}
	} else {
		patterns = []string{
			filepath.Join(home, ".local", "share", "JetBrains", "Toolbox", "scripts", "idea*"),
			filepath.Join(home, ".local", "share", "JetBrains", "Toolbox", "apps", "intellij-idea*", "bin", "idea"),
			filepath.Join(home, ".local", "share", "JetBrains", "Toolbox", "apps", "intellij-idea*", "bin", "idea.sh"),
			"/opt/idea*/bin/idea.sh",
			"/snap/bin/intellij-idea-*",
		}
	}
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[len(matches)-1]
		}
	}
	return ""
}

// OpenTerminal starts args in a new terminal window inside dir.
func OpenTerminal(dir string, args ...string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("wt"); err == nil {
			cmd = exec.Command("wt", append([]string{"-d", dir}, args...)...)
		} else {
			cmd = exec.Command("cmd", append([]string{"/c", "start", "", "cmd", "/k"}, args...)...)
		}
	} else {
		candidates := [][]string{
			{os.Getenv("TERMINAL"), "-e"},
			{"x-terminal-emulator", "-e"},
			{"gnome-terminal", "--"},
			{"konsole", "-e"},
			{"xterm", "-e"},
		}
		for _, c := range candidates {
			if c[0] == "" {
				continue
			}
			if _, err := exec.LookPath(c[0]); err == nil {
				cmd = exec.Command(c[0], append([]string{c[1]}, args...)...)
				break
			}
		}
		if cmd == nil {
			return errors.New("kein Terminal gefunden (TERMINAL setzen)")
		}
	}
	cmd.Dir = dir
	return cmd.Start()
}

type GitInfo struct {
	IsRepo bool
	Branch string
	Dirty  int
	WebURL string // browser URL of origin, if it is a known forge
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimRight(string(out), "\r\n"), err
}

func GitStatus(dir string) GitInfo {
	branch, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return GitInfo{}
	}
	info := GitInfo{IsRepo: true, Branch: branch}
	if st, err := git(dir, "status", "--porcelain"); err == nil && st != "" {
		info.Dirty = len(strings.Split(st, "\n"))
	}
	if remote, err := git(dir, "remote", "get-url", "origin"); err == nil {
		info.WebURL = WebURL(remote)
	}
	return info
}

// WebURL turns a git remote (ssh or https) into a browser URL.
func WebURL(remote string) string {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if strings.HasPrefix(r, "git@") {
		r = "https://" + strings.Replace(strings.TrimPrefix(r, "git@"), ":", "/", 1)
	}
	if u, err := url.Parse(r); err == nil && strings.HasPrefix(u.Scheme, "http") {
		u.User = nil
		return u.String()
	}
	return ""
}

func GitHubSearchURL(q string) string {
	return "https://github.com/search?type=repositories&q=" + url.QueryEscape(q)
}

func GitDiff(dir string) (string, error) {
	status, err := git(dir, "status", "--short", "--branch")
	if err != nil {
		return status, err
	}
	diff, err := git(dir, "diff", "HEAD")
	if diff == "" {
		diff = "(keine Änderungen an versionierten Dateien)"
	}
	return status + "\n\n" + diff, err
}

func GitPull(dir string) (string, error) {
	return git(dir, "pull", "--ff-only")
}
