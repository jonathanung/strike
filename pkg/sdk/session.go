package sdk

import (
	"errors"
	"io"
	"os"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// ReadSession loads every event from a durable session JSONL log (the same
// format written under ~/.strike/sessions/<id>.jsonl).
func ReadSession(path string) ([]protocol.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := NewEventDecoder(f)
	var events []protocol.Event
	for {
		ev, err := dec.Decode()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events, nil
			}
			return nil, err
		}
		events = append(events, ev)
	}
}

// WriteSession writes events as JSONL envelopes to path (truncate/create).
// Useful for fixtures and offline tooling; the stock CLI uses internal/session.
func WriteSession(path string, events []protocol.Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	for _, ev := range events {
		if err := WriteEvent(f, ev); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
