package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func TestWebSocketMessageLimitAcrossFragments(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close(); _ = clientConn.Close() })
	ws := &wsConn{conn: serverConn, bufr: bufio.NewReader(serverConn)}
	done := make(chan error, 1)
	go func() {
		_, err := ws.ReadText()
		done <- err
	}()

	writeTestFrame(t, clientConn, false, 0x1, make([]byte, maxWSMessage/2+1))
	writeTestFrame(t, clientConn, true, 0x0, make([]byte, maxWSMessage/2))
	if err := <-done; err == nil || !strings.Contains(err.Error(), "message too large") {
		t.Fatalf("ReadText error = %v", err)
	}
}

func writeTestFrame(t *testing.T, conn net.Conn, fin bool, opcode byte, payload []byte) {
	t.Helper()
	first := opcode
	if fin {
		first |= 0x80
	}
	header := []byte{first, 0x80 | 127}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
	header = append(header, size[:]...)
	mask := [4]byte{1, 2, 3, 4}
	header = append(header, mask[:]...)
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketRejectsInvalidClientFrames(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
		want  string
	}{
		{name: "unmasked", frame: []byte{0x81, 0x00}, want: "not masked"},
		{name: "RSV", frame: []byte{0xC1, 0x80, 1, 2, 3, 4}, want: "reserved bits"},
		{name: "fragmented control", frame: []byte{0x09, 0x80, 1, 2, 3, 4}, want: "fragmented"},
		{name: "oversized control", frame: []byte{0x89, 0xFE}, want: "control frame too large"},
		{name: "invalid close payload", frame: []byte{0x88, 0x81, 1, 2, 3, 4, 1}, want: "invalid websocket close payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &wsConn{bufr: bufio.NewReader(bytes.NewReader(tt.frame))}
			_, _, _, err := ws.readFrame()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readFrame error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWebSocketRejectsUnsupportedMessagePayloads(t *testing.T) {
	tests := []struct {
		name    string
		opcode  byte
		payload []byte
		want    string
	}{
		{name: "binary", opcode: 0x2, payload: []byte("not text"), want: "binary messages are not supported"},
		{name: "invalid UTF-8", opcode: 0x1, payload: []byte{0xff}, want: "not valid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &wsConn{bufr: bufio.NewReader(bytes.NewReader(maskedTestFrame(tt.opcode, tt.payload)))}
			_, err := ws.ReadText()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReadText error = %v, want %q", err, tt.want)
			}
		})
	}
}

func maskedTestFrame(opcode byte, payload []byte) []byte {
	mask := [4]byte{1, 2, 3, 4}
	frame := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	return frame
}
