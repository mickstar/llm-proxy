package notify

import "testing"

func TestQuoteEscapesAppleScript(t *testing.T) {
	cases := map[string]string{
		`secret`:        `"secret"`,
		`a"b`:           `"a\"b"`,
		`a\b`:           `"a\\b"`,
		"line1\nline2":  `"line1 line2"`,
		"line1\r\nline": `"line1 line"`,
	}
	for in, want := range cases {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}
