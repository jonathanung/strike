package server

import (
	"strings"

	"rsc.io/qr"
)

// QRBlocks renders text as a UTF-8 block QR for a terminal.
// Modules are two columns wide so the code stays roughly square.
// An empty string means the encoder rejected the text.
func QRBlocks(text string) string {
	code, err := qr.Encode(text, qr.M)
	if err != nil || code == nil {
		return ""
	}
	const quiet = 2
	n := code.Size + quiet*2
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		for x := 0; x < n; x++ {
			top := qrModule(code, x, y, quiet)
			bot := false
			if y+1 < n {
				bot = qrModule(code, x, y+1, quiet)
			}
			switch {
			case top && bot:
				b.WriteString("██")
			case top:
				b.WriteString("▀▀")
			case bot:
				b.WriteString("▄▄")
			default:
				b.WriteString("  ")
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func qrModule(code *qr.Code, x, y, quiet int) bool {
	x -= quiet
	y -= quiet
	if x < 0 || y < 0 || x >= code.Size || y >= code.Size {
		return false
	}
	return code.Black(x, y)
}
