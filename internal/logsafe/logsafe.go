// Package logsafe neutralizes untrusted text before it's logged, so a
// request path, a device hostname or a remote feed can't forge log lines
// (CWE-117).
package logsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen caps a cleaned value, so one hostile value can't flood the log.
const MaxLen = 512

// Clean escapes line breaks, replaces other control characters (including
// tabs, escape sequences and Unicode line/paragraph separators) with '?',
// and truncates to MaxLen runes.
func Clean(s string) string {
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || r == ' ' || r == ' ' {
			return '?'
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > MaxLen {
		n := 0
		for i := range s {
			if n == MaxLen {
				return s[:i] + "…"
			}
			n++
		}
	}
	return s
}

// Printf returns a printf-style function that cleans the formatted message
// and passes it to out. Use it to wrap loggers that see untrusted text.
func Printf(out func(msg string)) func(format string, args ...any) {
	return func(format string, args ...any) {
		out(Clean(fmt.Sprintf(format, args...)))
	}
}
