package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Max line size for JSONL event/op streams. Matches internal/session so
// multimodal user.message lines with multi-MiB base64 images still decode.
const maxJSONLLine = 32 << 20

// WriteEvent encodes one event as a single JSONL envelope line on w.
func WriteEvent(w io.Writer, ev protocol.Event) error {
	env, err := protocol.Wrap(ev)
	if err != nil {
		return err
	}
	return writeJSONLine(w, env)
}

// WriteOp encodes one op as a single JSON OpEnvelope line on w.
func WriteOp(w io.Writer, op protocol.Op) error {
	env, err := protocol.WrapOp(op)
	if err != nil {
		return err
	}
	return writeJSONLine(w, env)
}

func writeJSONLine(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// DecodeEventLine parses one JSONL event envelope line into an Event.
func DecodeEventLine(line []byte) (protocol.Event, error) {
	var env protocol.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	return env.Decode()
}

// DecodeOpLine parses one JSON op envelope line into an Op.
func DecodeOpLine(line []byte) (protocol.Op, error) {
	var env protocol.OpEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	return env.Decode()
}

// EventDecoder reads JSONL event envelopes from r.
type EventDecoder struct {
	sc  *bufio.Scanner
	err error
	n   int
}

// NewEventDecoder returns a decoder with a buffer large enough for session
// transcripts that include image attachments.
func NewEventDecoder(r io.Reader) *EventDecoder {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLine)
	return &EventDecoder{sc: sc}
}

// Decode reads the next event. Returns io.EOF when the stream ends cleanly.
// Lines with type "session.header" (session log schema marker) are skipped.
func (d *EventDecoder) Decode() (protocol.Event, error) {
	if d.err != nil {
		return nil, d.err
	}
	for {
		if !d.sc.Scan() {
			if err := d.sc.Err(); err != nil {
				d.err = err
				return nil, err
			}
			d.err = io.EOF
			return nil, io.EOF
		}
		d.n++
		raw := d.sc.Bytes()
		if isSessionLogHeaderLine(raw) {
			continue
		}
		ev, err := DecodeEventLine(raw)
		if err != nil {
			d.err = fmt.Errorf("sdk: event line %d: %w", d.n, err)
			return nil, d.err
		}
		return ev, nil
	}
}

func isSessionLogHeaderLine(raw []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Type == "session.header"
}

// Line is the 1-based index of the last successfully scanned line (0 before
// the first Decode).
func (d *EventDecoder) Line() int { return d.n }

// OpDecoder reads JSONL op envelopes from r.
type OpDecoder struct {
	sc  *bufio.Scanner
	err error
	n   int
}

// NewOpDecoder returns a decoder for op envelope streams.
func NewOpDecoder(r io.Reader) *OpDecoder {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxJSONLLine)
	return &OpDecoder{sc: sc}
}

// Decode reads the next op. Returns io.EOF when the stream ends cleanly.
func (d *OpDecoder) Decode() (protocol.Op, error) {
	if d.err != nil {
		return nil, d.err
	}
	if !d.sc.Scan() {
		if err := d.sc.Err(); err != nil {
			d.err = err
			return nil, err
		}
		d.err = io.EOF
		return nil, io.EOF
	}
	d.n++
	op, err := DecodeOpLine(d.sc.Bytes())
	if err != nil {
		d.err = fmt.Errorf("sdk: op line %d: %w", d.n, err)
		return nil, d.err
	}
	return op, nil
}

// ConnectJSONL returns a Client that writes ops as JSONL OpEnvelopes to opsOut
// and reads event envelopes from eventsIn on a background goroutine.
//
// The Events channel closes when eventsIn hits EOF or a decode error occurs.
// Call [Client.Close] to stop accepting further pump deliveries into a full
// buffer (it does not close opsOut or eventsIn). Close never blocks on a
// stuck read: close eventsIn (or the underlying connection) so the pump can
// finish, then optionally call Close again or range Events until it closes
// to observe a decode error via Close's return value once the pump has exited.
//
// opsOut writes are serialized. Concurrent Send is safe.
func ConnectJSONL(opsOut io.Writer, eventsIn io.Reader) *Client {
	if opsOut == nil {
		panic("sdk: nil ops writer")
	}
	if eventsIn == nil {
		panic("sdk: nil events reader")
	}

	events := make(chan protocol.Event, 64)
	var (
		writeMu  sync.Mutex
		once     sync.Once
		done     = make(chan struct{})
		pumpDone = make(chan struct{})
		pumpErr  error
	)

	go func() {
		defer close(pumpDone)
		defer close(events)
		dec := NewEventDecoder(eventsIn)
		for {
			ev, err := dec.Decode()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					pumpErr = err
				}
				return
			}
			select {
			case events <- ev:
			case <-done:
				return
			}
		}
	}()

	c := &Client{events: events}
	c.sendOp = func(ctx context.Context, op protocol.Op) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		env, err := protocol.WrapOp(op)
		if err != nil {
			return err
		}
		data, err := json.Marshal(env)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if err := ctx.Err(); err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = opsOut.Write(data)
		return err
	}
	c.closer = func() error {
		once.Do(func() { close(done) })
		// Non-blocking: only report pumpErr when the reader has already finished.
		// Callers that need a hard stop must close eventsIn so Decode unblocks.
		select {
		case <-pumpDone:
			return pumpErr
		default:
			return nil
		}
	}
	return c
}
