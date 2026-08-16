package config

import (
	"bytes"
	"fmt"
)

// stripJSONC removes // line comments and /* block comments */ outside of
// JSON strings so encoding/json can parse .jsonc files.
func stripJSONC(data []byte) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(data))
	i := 0
	for i < len(data) {
		c := data[i]
		// String literal — copy through, honoring escapes.
		if c == '"' {
			out.WriteByte(c)
			i++
			for i < len(data) {
				ch := data[i]
				out.WriteByte(ch)
				i++
				if ch == '\\' && i < len(data) {
					out.WriteByte(data[i])
					i++
					continue
				}
				if ch == '"' {
					break
				}
			}
			continue
		}
		// Line comment
		if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			i += 2
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment
		if c == '/' && i+1 < len(data) && data[i+1] == '*' {
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			if i+1 >= len(data) {
				return nil, fmt.Errorf("unterminated block comment")
			}
			i += 2
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.Bytes(), nil
}
