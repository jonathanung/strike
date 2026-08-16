package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

// rootSlot is one live parent session: engine + durable bind + tool CWD.
type rootSlot struct {
	id      string
	eng     *engine.Engine
	bound   session.Bound
	workDir string
	wtClose func() error
	// wtNotice is a soft-fail message from worktree bind (e.g. non-git cwd).
	wtNotice string
	cancel   context.CancelFunc
	done     chan struct{}
}

// rootSpawner creates additional in-process root engines sharing backend deps.
type rootSpawner func(resumeID string) (*rootSlot, error)

// multiRootHub implements host.Roots and bridges multiple engines to one TUI
// ops/events pair. Ops always go to the active root; events from every live
// root are muxed (each engine already stamps Correlation.SessionID).
type multiRootHub struct {
	mu     sync.Mutex
	active string
	slots  map[string]*rootSlot
	// order is spawn/open insertion order for LiveIDs. Activate must not
	// reorder so the agents pane stays stable when switching sessions (#865).
	order []string

	ops    chan protocol.Op
	events chan protocol.Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	spawn  rootSpawner
	files  host.Files // optional; workdir updated on Activate
	shell  host.Shell // optional; workdir updated on Activate
	closed bool
}

// newMultiRootHub owns the first root slot and starts op routing. Caller must
// Close the hub (stops engines, closes binds, runs worktree cleanup).
func newMultiRootHub(first *rootSlot, spawn rootSpawner, files host.Files, shell host.Shell) *multiRootHub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &multiRootHub{
		active: first.id,
		slots:  map[string]*rootSlot{},
		ops:    make(chan protocol.Op, 64),
		events: make(chan protocol.Event, 256),
		ctx:    ctx,
		cancel: cancel,
		spawn:  spawn,
		files:  files,
		shell:  shell,
	}
	h.startSlot(first)
	h.wg.Add(1)
	go h.routeOps()
	return h
}

func (h *multiRootHub) Ops() chan<- protocol.Op       { return h.ops }
func (h *multiRootHub) Events() <-chan protocol.Event { return h.events }
func (h *multiRootHub) ActiveID() string              { return h.activeLocked() }
func (h *multiRootHub) WorkDir(id string) string      { return h.workDirLocked(id) }

func (h *multiRootHub) activeLocked() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

func (h *multiRootHub) workDirLocked(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		id = h.active
	}
	if s, ok := h.slots[id]; ok {
		return s.workDir
	}
	return ""
}

func (h *multiRootHub) LiveIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.order) == 0 {
		return nil
	}
	out := make([]string, 0, len(h.order))
	for _, id := range h.order {
		if _, ok := h.slots[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

func (h *multiRootHub) Activate(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session id is empty")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("session hub is closed")
	}
	slot, ok := h.slots[id]
	if !ok {
		return fmt.Errorf("session %q is not live", id)
	}
	h.active = id
	h.syncWorkDir(slot.workDir)
	return nil
}

func (h *multiRootHub) syncWorkDir(workDir string) {
	if h.files != nil {
		local.SetFilesWorkDir(h.files, workDir)
	}
	if h.shell != nil {
		local.SetShellWorkDir(h.shell, workDir)
	}
}

func (h *multiRootHub) Spawn() (string, error) {
	if h.spawn == nil {
		return "", fmt.Errorf("spawn unavailable")
	}
	slot, err := h.spawn("")
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.teardownSlot(slot)
		return "", fmt.Errorf("session hub is closed")
	}
	if _, exists := h.slots[slot.id]; exists {
		h.mu.Unlock()
		h.teardownSlot(slot)
		return "", fmt.Errorf("session %q already live", slot.id)
	}
	h.startSlotLocked(slot)
	h.active = slot.id
	h.syncWorkDir(slot.workDir)
	h.mu.Unlock()
	return slot.id, nil
}

func (h *multiRootHub) Open(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session id is empty")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("session hub is closed")
	}
	if slot, ok := h.slots[id]; ok {
		h.active = id
		h.syncWorkDir(slot.workDir)
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	if h.spawn == nil {
		return fmt.Errorf("open unavailable")
	}
	slot, err := h.spawn(id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.teardownSlot(slot)
		return fmt.Errorf("session hub is closed")
	}
	if existing, ok := h.slots[id]; ok {
		// Race: another Open won; drop ours and activate existing.
		h.mu.Unlock()
		h.teardownSlot(slot)
		return h.Activate(existing.id)
	}
	h.startSlotLocked(slot)
	h.active = slot.id
	h.syncWorkDir(slot.workDir)
	h.mu.Unlock()
	return nil
}

func (h *multiRootHub) Interrupt(id string) error {
	id = strings.TrimSpace(id)
	h.mu.Lock()
	if id == "" {
		id = h.active
	}
	slot, ok := h.slots[id]
	h.mu.Unlock()
	if !ok || slot == nil || slot.eng == nil {
		return fmt.Errorf("session %q is not live", id)
	}
	ops := slot.eng.Ops()
	select {
	case ops <- protocol.Interrupt{}:
		return nil
	case <-h.ctx.Done():
		return fmt.Errorf("session hub is closed")
	default:
		// Buffer full: drop rather than block the TUI on a stuck engine.
		return fmt.Errorf("session %q interrupt dropped (busy)", id)
	}
}

// Close stops every live root, drains tees, and closes durable binds.
func (h *multiRootHub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	slots := make([]*rootSlot, 0, len(h.slots))
	for _, s := range h.slots {
		slots = append(slots, s)
	}
	h.slots = map[string]*rootSlot{}
	h.order = nil
	h.mu.Unlock()

	h.cancel()
	// Unblock routeOps if waiting on ops channel writers only; TUI closes ops
	// by returning — we don't close ops here (TUI may still send during quit).
	for _, s := range slots {
		if s.cancel != nil {
			s.cancel()
		}
	}
	for _, s := range slots {
		if s.done != nil {
			<-s.done
		}
	}
	h.wg.Wait()
	close(h.events)

	var first error
	for _, s := range slots {
		if err := s.bound.Close(); err != nil && first == nil {
			first = err
		}
		if s.wtClose != nil {
			if err := s.wtClose(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (h *multiRootHub) startSlot(slot *rootSlot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startSlotLocked(slot)
}

func (h *multiRootHub) startSlotLocked(slot *rootSlot) {
	if slot == nil || slot.eng == nil {
		return
	}
	slotCtx, slotCancel := context.WithCancel(h.ctx)
	slot.cancel = slotCancel
	slot.done = make(chan struct{})
	if _, exists := h.slots[slot.id]; !exists {
		h.order = append(h.order, slot.id)
	}
	h.slots[slot.id] = slot

	eng := slot.eng
	bound := slot.bound
	events := eng.Events()
	done := slot.done

	h.wg.Add(2)
	go func() {
		defer h.wg.Done()
		eng.Run(slotCtx)
	}()
	go func() {
		defer h.wg.Done()
		defer close(done)
		forwarding := true
		persistOK := true
		for ev := range events {
			if persistOK {
				if err := bound.Append(ev); err != nil {
					// Stop side effects; do not forward unrecorded events.
					persistOK = false
					forwarding = false
					if slotCancel != nil {
						slotCancel()
					}
					continue
				}
			}
			if !forwarding {
				continue
			}
			select {
			case <-h.ctx.Done():
				forwarding = false
			default:
			}
			if !forwarding {
				continue
			}
			select {
			case h.events <- ev:
			case <-h.ctx.Done():
				forwarding = false
			}
		}
	}()
}

func (h *multiRootHub) teardownSlot(slot *rootSlot) {
	if slot == nil {
		return
	}
	if slot.cancel != nil {
		slot.cancel()
	}
	if slot.eng != nil {
		// Wait briefly for Events to close by cancelling Run; drain if needed.
		if slot.done != nil {
			select {
			case <-slot.done:
			default:
				// Engine may not have been started; close bind anyway.
			}
		}
	}
	_ = slot.bound.Close()
	if slot.wtClose != nil {
		_ = slot.wtClose()
	}
}

func (h *multiRootHub) routeOps() {
	defer h.wg.Done()
	for {
		select {
		case <-h.ctx.Done():
			return
		case op, ok := <-h.ops:
			if !ok {
				return
			}
			h.mu.Lock()
			slot := h.slots[h.active]
			h.mu.Unlock()
			if slot == nil || slot.eng == nil {
				continue
			}
			ops := slot.eng.Ops()
			select {
			case ops <- op:
			case <-h.ctx.Done():
				return
			}
		}
	}
}

// Ensure multiRootHub satisfies host.Roots at compile time.
var _ host.Roots = (*multiRootHub)(nil)
