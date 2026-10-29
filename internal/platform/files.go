package platform

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileEntry is a file below a session directory. Path is relative to that
// directory and uses forward slashes.
type FileEntry struct {
	Path   string
	Status string // git short status: "M", "A", "D", "?", "R" or ""
}

const maxFiles = 20000

var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "__pycache__": true, ".idea": true}

// ListFiles lists the files of dir. In a git repository it honours
// .gitignore and attaches the git status; otherwise it walks the directory.
func ListFiles(dir string) ([]FileEntry, error) {
	if out, err := git(dir, "-c", "core.quotePath=false", "ls-files", "--cached", "--others", "--exclude-standard"); err == nil {
		status := gitShortStatus(dir)
		seen := map[string]bool{}
		var files []FileEntry
		for _, p := range strings.Split(out, "\n") {
			if p = strings.TrimSpace(p); p == "" || seen[p] {
				continue
			}
			seen[p] = true
			files = append(files, FileEntry{Path: p, Status: status[p]})
		}
		// Deleted files are no longer listed by ls-files --others.
		for p, st := range status {
			if !seen[p] {
				files = append(files, FileEntry{Path: p, Status: st})
			}
		}
		sortFiles(files)
		return files, nil
	}

	var files []FileEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, FileEntry{Path: filepath.ToSlash(rel)})
		if len(files) >= maxFiles {
			return fs.SkipAll
		}
		return nil
	})
	sortFiles(files)
	return files, err
}

func sortFiles(files []FileEntry) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

// gitShortStatus maps paths (relative to dir) to a one-letter status.
func gitShortStatus(dir string) map[string]string {
	out, err := git(dir, "-c", "core.quotePath=false", "-c", "color.status=false",
		"status", "--short", "--untracked-files=all", "--", ".")
	status := map[string]string{}
	if err != nil {
		return status
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		xy, p := line[:2], line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.Trim(p, `"`)
		status[p] = statusLetter(xy)
	}
	return status
}

func statusLetter(xy string) string {
	switch {
	case xy == "??":
		return "?"
	case strings.ContainsRune(xy, 'D'):
		return "D"
	case strings.ContainsRune(xy, 'R'):
		return "R"
	case strings.ContainsRune(xy, 'A'):
		return "A"
	default:
		return "M"
	}
}

// ReadPreview returns up to 256 KB of a text file with line numbers.
func ReadPreview(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return "(Binärdatei)", nil
	}
	text := strings.ReplaceAll(string(buf), "\t", "    ")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%5d  %s\n", i+1, l)
	}
	if n == len(buf) && n == 256*1024 {
		b.WriteString("\n… (gekürzt)")
	}
	return b.String(), nil
}

// GitDiffFile shows the diff of one file against HEAD.
func GitDiffFile(dir, rel string) (string, error) {
	out, err := git(dir, "diff", "HEAD", "--", rel)
	if err == nil && out == "" {
		out = "(keine Änderungen gegenüber HEAD – neue oder unveränderte Datei)"
	}
	return out, err
}
