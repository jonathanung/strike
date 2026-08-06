package sdk_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)

func TestWriteDecodeEventRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := sdk.WriteEvent(&buf, protocol.TextDelta{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := sdk.WriteEvent(&buf, protocol.TurnCompleted{StopReason: "end_turn"}); err != nil {
		t.Fatal(err)
	}

	dec := sdk.NewEventDecoder(&buf)
	ev, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	td, ok := ev.(protocol.TextDelta)
	if !ok || td.Text != "hi" {
		t.Fatalf("ev = %#v", ev)
	}
	ev, err = dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ev.(protocol.TurnCompleted); !ok {
		t.Fatalf("ev = %#v", ev)
	}
	if _, err := dec.Decode(); err != io.EOF {
		t.Fatalf("err = %v", err)
	}
	if dec.Line() != 2 {
		t.Fatalf("line = %d", dec.Line())
	}
}

func TestWriteDecodeOpRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := sdk.WriteOp(&buf, protocol.UserInput{Text: "ping"}); err != nil {
		t.Fatal(err)
	}
	if err := sdk.WriteOp(&buf, protocol.Interrupt{}); err != nil {
		t.Fatal(err)
	}

	dec := sdk.NewOpDecoder(&buf)
	op, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	in, ok := op.(protocol.UserInput)
	if !ok || in.Text != "ping" {
		t.Fatalf("op = %#v", op)
	}
	op, err = dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := op.(protocol.Interrupt); !ok {
		t.Fatalf("op = %#v", op)
	}
}

func TestDecodeEventLineBadJSON(t *testing.T) {
	if _, err := sdk.DecodeEventLine([]byte(`{`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestConnectJSONL(t *testing.T) {
	opsR, opsW := io.Pipe()
	evR, evW := io.Pipe()
	defer opsR.Close()
	defer evW.Close()

	client := sdk.ConnectJSONL(opsW, evR)
	defer func() {
		_ = opsW.Close()
		_ = evR.Close()
		_ = client.Close()
	}()

	// Peer: read one op, write two events.
	done := make(chan error, 1)
	go func() {
		dec := sdk.NewOpDecoder(opsR)
		op, err := dec.Decode()
		if err != nil {
			done <- err
			return
		}
		if in, ok := op.(protocol.UserInput); !ok || in.Text != "go" {
			done <- errString("bad op")
			return
		}
		if err := sdk.WriteEvent(evW, protocol.TextDelta{Text: "pong"}); err != nil {
			done <- err
			return
		}
		if err := sdk.WriteEvent(evW, protocol.TurnCompleted{StopReason: "end_turn"}); err != nil {
			done <- err
			return
		}
		_ = evW.Close()
		done <- nil
	}()

	result, err := client.RunTurn(context.Background(), sdk.Turn{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "pong" {
		t.Fatalf("text = %q", result.Text)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer timeout")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestConnectJSONLSendWritesEnvelope(t *testing.T) {
	var opsBuf bytes.Buffer
	evR, evW := io.Pipe()
	_ = evW.Close() // immediate EOF on events

	client := sdk.ConnectJSONL(&opsBuf, evR)
	if err := client.Send(context.Background(), protocol.SetEffort{Level: protocol.EffortLow}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()

	line := strings.TrimSpace(opsBuf.String())
	op, err := sdk.DecodeOpLine([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	se, ok := op.(protocol.SetEffort)
	if !ok || se.Level != protocol.EffortLow {
		t.Fatalf("op = %#v", op)
	}
}

func TestEventDecoderSkipsSessionHeader(t *testing.T) {
	raw := `{"type":"session.header","schemaVersion":1,"time":"2020-01-01T00:00:00Z"}` + "\n" +
		`{"type":"user.message","time":"2020-01-01T00:00:01Z","v":"1.4.0","data":{"text":"hi"}}` + "\n"
	dec := sdk.NewEventDecoder(strings.NewReader(raw))
	ev, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	um, ok := ev.(protocol.UserMessage)
	if !ok || um.Text != "hi" {
		t.Fatalf("ev = %#v", ev)
	}
	if _, err := dec.Decode(); err != io.EOF {
		t.Fatalf("err = %v", err)
	}
}
