//go:build !darwin || !cgo

package tui

import "errors"

func readClipboardImage() ([]byte, error) {
	return nil, errors.New("clipboard image attachments require macOS with cgo enabled")
}
