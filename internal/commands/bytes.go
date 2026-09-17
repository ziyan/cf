package commands

import (
	"bytes"
	"unicode/utf8"
)

func bytesContains(text, wanted []byte) bool { return bytes.Contains(text, wanted) }

// bytesContainsFold reports whether text holds wanted ignoring case, without
// the lowercase copy of text that bytes.ToLower would make for every line in
// the archive. The caller lowercases wanted first.
func bytesContainsFold(text, wanted []byte) bool {
	if len(wanted) == 0 {
		return true
	}
	if !isAscii(wanted) {
		return bytes.Contains(bytes.ToLower(text), wanted)
	}
	first := wanted[0]
	upper := first
	if 'a' <= first && first <= 'z' {
		upper = first - ('a' - 'A')
	}
	for index := 0; index+len(wanted) <= len(text); index++ {
		character := text[index]
		if character != first && character != upper && character < utf8.RuneSelf {
			continue
		}
		if bytes.EqualFold(text[index:index+len(wanted)], wanted) {
			return true
		}
	}
	return false
}

func isAscii(text []byte) bool {
	for _, character := range text {
		if character >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
