package provider

// NormalizeStream enforces the terminal stream contract on an inbound channel:
//
//   - At most one terminal event (EventDone or EventError) is forwarded.
//   - Events after the first terminal are dropped.
//   - If the channel closes with no terminal event, a single EventError with
//     ErrIncompleteStream is emitted before close.
//
// The returned channel is always closed after its terminal event.
func NormalizeStream(in <-chan StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		terminated := false
		for ev := range in {
			if terminated {
				continue
			}
			switch ev.Type {
			case EventDone, EventError:
				out <- ev
				terminated = true
			default:
				out <- ev
			}
		}
		if !terminated {
			out <- StreamEvent{Type: EventError, Err: ErrIncompleteStream}
		}
	}()
	return out
}
