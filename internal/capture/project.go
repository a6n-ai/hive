package capture

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveProject returns the git root containing cwd (symlinks resolved), or
// the resolved cwd when it is not inside a repository. It never runs git.
func ResolveProject(cwd string) string {
	dir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		dir = filepath.Clean(cwd)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if parent := filepath.Dir(d); parent == d {
			return dir
		}
	}
}

// underAny reports whether project is one of paths or inside one of them.
func underAny(project string, paths []string) bool {
	for _, p := range paths {
		p = filepath.Clean(p)
		if project == p || strings.HasPrefix(project, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
