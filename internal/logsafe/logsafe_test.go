package logsafe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean(t *testing.T) {
	cases := map[string]string{
		"/api/v1/x":                          "/api/v1/x",
		"/x\nlevel=INFO msg=\"admin login\"": `/x\nlevel=INFO msg="admin login"`,
		"a\r\nb":                             `a\r\nb`,
		"tab\there":                          "tab?here",
		"esc\x1b[31mred":                     "esc?[31mred",
		"line sep":                           "line?sep",
		"bad\xffutf8":                        "bad?utf8",
		"héllo 😀":                            "héllo 😀",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", MaxLen+100)
	if got := Clean(long); utf8.RuneCountInString(got) != MaxLen+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("not truncated: %d runes", utf8.RuneCountInString(got))
	}
}

func TestPrintf(t *testing.T) {
	var got string
	Printf(func(m string) { got = m })("slide %s: %v", "id\nforged", "err")
	if got != `slide id\nforged: err` {
		t.Fatalf("got %q", got)
	}
}

func FuzzClean(f *testing.F) {
	f.Add("a\nb\rc\x00d ")
	f.Fuzz(func(t *testing.T, s string) {
		out := Clean(s)
		if strings.ContainsAny(out, "\r\n  ") {
			t.Fatalf("line break survived: %q", out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8 out: %q", out)
		}
		if utf8.RuneCountInString(out) > MaxLen+1 {
			t.Fatalf("too long: %d", utf8.RuneCountInString(out))
		}
	})
}
