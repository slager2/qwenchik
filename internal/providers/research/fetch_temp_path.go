package research

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveFetchTempPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func fetchTempRootContains(root, target string) bool {
	for _, rootAlias := range fetchTempPathAliases(root) {
		for _, targetAlias := range fetchTempPathAliases(target) {
			relative, err := filepath.Rel(rootAlias, targetAlias)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func fetchTempPathAliases(path string) []string {
	path = filepath.Clean(path)
	aliases := []string{path}
	current := path
	suffix := []string{}
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err == nil {
				for index := len(suffix) - 1; index >= 0; index-- {
					resolved = filepath.Join(resolved, suffix[index])
				}
				resolved = filepath.Clean(resolved)
				if resolved != path {
					aliases = append(aliases, resolved)
				}
			}
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	return aliases
}
