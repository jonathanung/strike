package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxWSMessage = 8 << 20
)

// wsConn is a minimal RFC6455 WebSocket (text + control) over a hijacked conn.
type wsConn struct {
	conn    net.Conn
	bufr    *bufio.Reader
	writeMu sync.Mutex
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("missing Upgrade: websocket")
	}
	if !headerContainsToken(r.Header.Get("Connection"), "Upgrade") {
		return nil, errors.New("missing Connection: Upgrade")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijack unsupported")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	accept := wsAcceptKey(key)
	_, _ = fmt.Fprintf(bufrw, "HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = fmt.Fprintf(bufrw, "Upgrade: websocket\r\n")
	_, _ = fmt.Fprintf(bufrw, "Connection: Upgrade\r\n")
	_, _ = fmt.Fprintf(bufrw, "Sec-WebSocket-Accept: %s\r\n", accept)
	_, _ = fmt.Fprintf(bufrw, "\r\n")
	if err := bufrw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	var br *bufio.Reader
	if bufrw.Reader != nil {
		br = bufrw.Reader
	} else {
		br = bufio.NewReader(conn)
	}
	return &wsConn{conn: conn, bufr: br}, nil
}

func wsAcceptKey(key string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, key+wsGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func headerContainsToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (c *wsConn) Close() error {
	_ = c.writeControl(0x8, nil)
	return c.conn.Close()
}

// ReadText reads the next complete text data message.
func (c *wsConn) ReadText() (string, error) {
	var payload []byte
	var started bool
	for {
		fin, opcode, data, err := c.readFrame()
		if err != nil {
			return "", err
		}
		switch opcode {
		case 0x1: // text start
			if started {
				return "", errors.New("new data frame before fragmented message completed")
			}
			if len(data) > maxWSMessage {
				return "", errors.New("websocket message too large")
			}
			payload = append(payload[:0], data...)
			started = true
			if fin {
				if !utf8.Valid(payload) {
					return "", errors.New("websocket text message is not valid UTF-8")
				}
				return string(payload), nil
			}
		case 0x0: // continuation
			if !started {
				return "", errors.New("unexpected continuation frame")
			}
			if len(payload) > maxWSMessage-len(data) {
				return "", errors.New("websocket message too large")
			}
			payload = append(payload, data...)
			if fin {
				if !utf8.Valid(payload) {
					return "", errors.New("websocket text message is not valid UTF-8")
				}
				return string(payload), nil
			}
		case 0x2:
			return "", errors.New("websocket binary messages are not supported")
		case 0x8:
			return "", io.EOF
		case 0x9:
			_ = c.writeControl(0xA, data)
		case 0xA:
			// pong
		default:
			return "", fmt.Errorf("unsupported websocket opcode %d", opcode)
		}
	}
}

// WriteText sends a single text data frame (server→client, unmasked).
func (c *wsConn) WriteText(s string) error {
	return c.writeFrame(0x1, []byte(s))
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	h := make([]byte, 2)
	if _, err = io.ReadFull(c.bufr, h); err != nil {
		return false, 0, nil, err
	}
	if h[0]&0x70 != 0 {
		return false, 0, nil, errors.New("websocket reserved bits are not supported")
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	if !masked {
		return false, 0, nil, errors.New("websocket client frame is not masked")
	}
	control := opcode&0x08 != 0
	if opcode != 0x0 && opcode != 0x1 && opcode != 0x2 && opcode != 0x8 && opcode != 0x9 && opcode != 0xA {
		return false, 0, nil, fmt.Errorf("unsupported websocket opcode %d", opcode)
	}
	if control && !fin {
		return false, 0, nil, errors.New("fragmented websocket control frame")
	}
	n := int(h[1] & 0x7f)
	if control && n > 125 {
		return false, 0, nil, errors.New("websocket control frame too large")
	}
	switch {
	case n == 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.bufr, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case n == 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.bufr, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n64 := binary.BigEndian.Uint64(ext[:])
		if n64 > maxWSMessage {
			return false, 0, nil, errors.New("websocket frame too large")
		}
		n = int(n64)
	}
	var mask [4]byte
	if _, err = io.ReadFull(c.bufr, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, n)
	if n > 0 {
		if _, err = io.ReadFull(c.bufr, payload); err != nil {
			return false, 0, nil, err
		}
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	if opcode == 0x8 && len(payload) == 1 {
		return false, 0, nil, errors.New("invalid websocket close payload")
	}
	if opcode == 0x8 && len(payload) >= 2 {
		code := binary.BigEndian.Uint16(payload[:2])
		if !validWSCloseCode(code) {
			return false, 0, nil, fmt.Errorf("invalid websocket close status code %d", code)
		}
		if !utf8.Valid(payload[2:]) {
			return false, 0, nil, errors.New("websocket close reason is not valid UTF-8")
		}
	}
	return fin, opcode, payload, nil
}

func validWSCloseCode(code uint16) bool {
	return code >= 1000 && code <= 1014 && code != 1004 && code != 1005 && code != 1006 || code >= 3000 && code <= 4999
}

func (c *wsConn) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return c.writeFrame(opcode, payload)
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	n := len(payload)
	var hdr []byte
	hdr = append(hdr, 0x80|opcode)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 1<<16:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		_, err := c.conn.Write(payload)
		return err
	}
	return nil
}
