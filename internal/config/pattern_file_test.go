package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePatternFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompileFilterPatternFileIsLiteralAndCaseSensitive(t *testing.T) {
	key := writePatternFile(t, filepath.Join(t.TempDir(), "k.pem"), "KEY.pem\nsecond.line\n")

	re, err := CompileFilter(Filter{Name: "k", PatternFile: key})
	if err != nil {
		t.Fatalf("CompileFilter: %v", err)
	}

	for _, sample := range []string{
		"KEY.pem\nsecond.line",     // no trailing newline in the body
		"x KEY.pem\nsecond.line y", // embedded
		"KEY.pem\r\nsecond.line",   // CRLF body
	} {
		if !re.MatchString(sample) {
			t.Errorf("no match for %q", sample)
		}
	}
	for _, sample := range []string{
		"key.pem\nsecond.line", // case-insensitive matching is off
		"KEYXpem\nsecond.line", // '.' is literal, not a wildcard
		"KEY.pem\nsecondXline", // '.' is literal on every line
		"KEY.pem",              // a prefix is not the whole file
	} {
		if re.MatchString(sample) {
			t.Errorf("unexpected match for %q", sample)
		}
	}
}

func TestCompileFilterPatternFileAlternation(t *testing.T) {
	dir := t.TempDir()
	writePatternFile(t, filepath.Join(dir, "a.pem"), "alpha")
	writePatternFile(t, filepath.Join(dir, "b.pem"), "beta")
	writePatternFile(t, filepath.Join(dir, "c.txt"), "gamma")

	re, err := CompileFilter(Filter{Name: "k", PatternFile: filepath.Join(dir, "*.pem")})
	if err != nil {
		t.Fatalf("CompileFilter: %v", err)
	}
	if !re.MatchString("has alpha in it") || !re.MatchString("has beta in it") {
		t.Error("glob did not cover every matching file")
	}
	if re.MatchString("has gamma in it") {
		t.Error("glob matched a file outside the spec")
	}
}

func TestCompileFilterInlinePatternUnchanged(t *testing.T) {
	re, err := CompileFilter(Filter{Name: "p", Pattern: "james brown"})
	if err != nil {
		t.Fatalf("CompileFilter: %v", err)
	}
	if !re.MatchString("JAMES BROWN") {
		t.Error("inline patterns lost their case-insensitive default")
	}
}

func TestExpandPatternFile(t *testing.T) {
	dir := t.TempDir()
	writePatternFile(t, filepath.Join(dir, "a.pem"), "alpha")
	writePatternFile(t, filepath.Join(dir, "b.pem"), "beta")
	if err := os.Mkdir(filepath.Join(dir, "sub.pem"), 0o700); err != nil {
		t.Fatal(err)
	}

	paths, err := expandPatternFile(filepath.Join(dir, "*.pem"))
	if err != nil {
		t.Fatalf("expandPatternFile: %v", err)
	}
	if len(paths) != 2 || paths[0] != filepath.Join(dir, "a.pem") || paths[1] != filepath.Join(dir, "b.pem") {
		t.Errorf("paths = %v, want the two regular files in sorted order", paths)
	}

	if _, err := expandPatternFile(filepath.Join(dir, "*.nope")); err == nil {
		t.Error("a glob matching nothing was accepted")
	}
	if _, err := expandPatternFile(dir); err == nil {
		t.Error("a directory spec was accepted")
	}
	if _, err := expandPatternFile(filepath.Join(dir, "*.pem") + "/nope"); err == nil {
		t.Error("a missing path was accepted")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := expandHome("~"); err != nil || got != home {
		t.Errorf("expandHome(~) = %q, %v; want %q", got, err, home)
	}
	if got, err := expandHome("~/keys/id_rsa"); err != nil || got != filepath.Join(home, "keys/id_rsa") {
		t.Errorf("expandHome(~/keys/id_rsa) = %q, %v", got, err)
	}
	if got, err := expandHome("keys/id_rsa"); err != nil || got != "keys/id_rsa" {
		t.Errorf("expandHome(keys/id_rsa) = %q, %v", got, err)
	}
}

func TestValidateFilterMatchSource(t *testing.T) {
	dir := t.TempDir()
	key := writePatternFile(t, filepath.Join(dir, "k.pem"), "secret")
	empty := writePatternFile(t, filepath.Join(dir, "empty"), "")
	big := filepath.Join(dir, "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxPatternFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		rule Filter
		want string
	}{
		{"both sources", Filter{Name: "x", Pattern: "a", PatternFile: key, Action: ActionBlock, Enabled: true}, "mutually exclusive"},
		{"no source", Filter{Name: "x", Action: ActionBlock, Enabled: true}, "required"},
		{"missing file", Filter{Name: "x", PatternFile: filepath.Join(dir, "nope"), Action: ActionBlock, Enabled: true}, "pattern_file"},
		{"empty file", Filter{Name: "x", PatternFile: empty, Action: ActionBlock, Enabled: true}, "empty"},
		{"oversize file", Filter{Name: "x", PatternFile: big, Action: ActionBlock, Enabled: true}, "larger than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Filters = []Filter{tc.rule}
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", tc.rule)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "x") {
				t.Errorf("error = %v, want it to name the filter", err)
			}
		})
	}

	valid := Default()
	valid.Filters = []Filter{{Name: "keys", PatternFile: filepath.Join(dir, "*.pem"), Action: ActionBlock, Enabled: true}}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid pattern_file filter rejected: %v", err)
	}
}
