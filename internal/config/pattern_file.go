package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MaxPatternFileBytes caps how much of a single file named by pattern_file is
// read. A glob that reaches a large file is a configuration mistake, not a
// reason to build a megabyte-sized pattern.
const MaxPatternFileBytes = 1 << 20

// CompileFilter compiles a filter's effective pattern: the inline Pattern, or
// the verbatim contents of the files named by PatternFile. File-derived
// patterns match literally and case-sensitively — a file's text is not
// regular-expression syntax, and base64 key bodies are case-significant — while
// inline patterns keep the case-insensitive default of CompilePattern.
func CompileFilter(f Filter) (*regexp.Regexp, error) {
	if f.PatternFile == "" {
		re, err := CompilePattern(f.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		return re, nil
	}
	src, err := filePatternSource(f.PatternFile)
	if err != nil {
		return nil, fmt.Errorf("pattern_file: %w", err)
	}
	// The (?-i) cancels the (?i) that CompilePattern prepends.
	re, err := CompilePattern("(?-i)" + src)
	if err != nil {
		return nil, fmt.Errorf("pattern_file: %w", err)
	}
	return re, nil
}

// filePatternSource turns a pattern_file spec into one RE2 source matching the
// verbatim contents of every file it names.
func filePatternSource(spec string) (string, error) {
	paths, err := expandPatternFile(spec)
	if err != nil {
		return "", err
	}
	alts := make([]string, 0, len(paths))
	for _, path := range paths {
		literal, err := literalFromFile(path)
		if err != nil {
			return "", err
		}
		alts = append(alts, literal)
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return "(?:" + strings.Join(alts, "|") + ")", nil
}

// expandPatternFile resolves a pattern_file spec into existing regular files:
// a leading ~ becomes the user's home directory, and a spec containing glob
// metacharacters expands to its sorted matches. Directories are skipped. A spec
// that names no regular file is an error, so a filter never compiles without a
// pattern and silently stops protecting its secret.
func expandPatternFile(spec string) ([]string, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errors.New("path is empty")
	}
	path, err := expandHome(spec)
	if err != nil {
		return nil, err
	}
	if !strings.ContainsAny(path, `*?[`) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s: is a directory", path)
		}
		return []string{path}, nil
	}
	matches, err := filepath.Glob(path)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && !info.IsDir() {
			paths = append(paths, match)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s: no files match", path)
	}
	return paths, nil
}

// expandHome expands a leading ~ or ~/ against the user's home directory.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// literalFromFile reads one file and builds its literal pattern source.
func literalFromFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxPatternFileBytes {
		return "", fmt.Errorf("%s: larger than %d bytes", path, MaxPatternFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > MaxPatternFileBytes {
		return "", fmt.Errorf("%s: larger than %d bytes", path, MaxPatternFileBytes)
	}
	literal, ok := literalFromBytes(data)
	if !ok {
		return "", fmt.Errorf("%s: file is empty", path)
	}
	return literal, nil
}

// literalFromBytes builds an RE2 source that matches data verbatim: line
// endings are normalized, trailing newlines are optional, and every
// metacharacter is escaped so the file is matched as text rather than as a
// pattern. It reports false when data holds only whitespace.
func literalFromBytes(data []byte) (string, bool) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = regexp.QuoteMeta(line)
	}
	return strings.Join(lines, `\r?\n`), true
}
