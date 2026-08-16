package tui

import (
	tea "charm.land/bubbletea/v2"
)

// modalPriority orders competing overlays. Lower values win when promoting
// from the queue. An open user modal is never replaced by a lower-or-equal
// priority ask; blocking asks queue behind it instead.
type modalPriority int

const (
	modalPriorityFatal modalPriority = iota
	modalPriorityBlocking
	modalPriorityUser
	modalPriorityInfo
)

func modalPriorityOf(m modal) modalPriority {
	if m == nil {
		return modalPriorityInfo
	}
	switch m.(type) {
	case *permissionModal, *questionModal:
		return modalPriorityBlocking
	case *doctorModal:
		return modalPriorityInfo
	default:
		return modalPriorityUser
	}
}

func modalRequestID(m modal) string {
	switch m := m.(type) {
	case *permissionModal:
		return m.req.RequestID
	case *questionModal:
		return m.req.RequestID
	default:
		return ""
	}
}

// presentBlockingModal shows a permission/question modal immediately when the
// top slot is free or already a lower-priority overlay. Otherwise it queues
// behind the protected user modal without losing that modal's state.
// Auto-approve is armed only when the modal is actually shown.
func (m *Model) presentBlockingModal(mod modal) tea.Cmd {
	if mod == nil {
		return nil
	}
	id := modalRequestID(mod)
	if id != "" {
		m.removeQueuedRequest(id)
		if curID := modalRequestID(m.modal); curID != "" && curID == id {
			// Same ask already visible — replace in place (fresh payload).
			m.modal = mod
			return m.armVisibleBlocking(mod)
		}
	}

	if m.modal == nil {
		m.modal = mod
		return m.armVisibleBlocking(mod)
	}

	curPrio := modalPriorityOf(m.modal)
	newPrio := modalPriorityOf(mod)
	// User-initiated modals keep the top slot and full state until dismissed.
	if curPrio == modalPriorityUser {
		m.enqueueModal(mod)
		return nil
	}
	// Informational overlays yield to blocking/fatal asks.
	if curPrio == modalPriorityInfo && newPrio < curPrio {
		m.modal = mod
		return m.armVisibleBlocking(mod)
	}
	// Top is already blocking/fatal: higher priority preempts; peers queue.
	if newPrio < curPrio {
		// Pause auto-approve on the displaced permission before hiding it.
		if pm, ok := m.modal.(*permissionModal); ok {
			pm.cancelCountdown()
		}
		m.enqueueModal(m.modal)
		m.modal = mod
		return m.armVisibleBlocking(mod)
	}
	m.enqueueModal(mod)
	return nil
}

func (m *Model) armVisibleBlocking(mod modal) tea.Cmd {
	pm, ok := mod.(*permissionModal)
	if !ok {
		return nil
	}
	return m.armPermissionAutoApprove(pm, pm.req.Permission)
}

func (m *Model) enqueueModal(mod modal) {
	if mod == nil {
		return
	}
	id := modalRequestID(mod)
	if id != "" {
		for i, q := range m.modalQueue {
			if modalRequestID(q) == id {
				m.modalQueue[i] = mod
				return
			}
		}
	}
	m.modalQueue = append(m.modalQueue, mod)
}

// removeQueuedRequest drops a queued ask by request id. Returns true if found.
func (m *Model) removeQueuedRequest(requestID string) bool {
	if requestID == "" || len(m.modalQueue) == 0 {
		return false
	}
	out := m.modalQueue[:0]
	found := false
	for _, q := range m.modalQueue {
		if modalRequestID(q) == requestID {
			found = true
			continue
		}
		out = append(out, q)
	}
	if found {
		// Re-slice into a fresh backing array when empty so len stays honest.
		if len(out) == 0 {
			m.modalQueue = nil
		} else {
			m.modalQueue = out
		}
	}
	return found
}

// resolveBlockingRequest closes a matching visible ask or drops it from the
// queue when it resolved while hidden. Promotes the next pending modal.
func (m *Model) resolveBlockingRequest(requestID string) tea.Cmd {
	if requestID == "" {
		return nil
	}
	if modalRequestID(m.modal) == requestID {
		// Cancel any in-flight countdown so a stale tick cannot fire.
		if pm, ok := m.modal.(*permissionModal); ok {
			pm.cancelCountdown()
		}
		m.modal = nil
		return m.promoteModalQueue()
	}
	m.removeQueuedRequest(requestID)
	return nil
}

// promoteModalQueue presents the highest-priority queued modal. FIFO within
// the same priority. Arms permission auto-approve only for newly visible asks.
func (m *Model) promoteModalQueue() tea.Cmd {
	if m.modal != nil || len(m.modalQueue) == 0 {
		return nil
	}
	bestIdx := 0
	bestPrio := modalPriorityOf(m.modalQueue[0])
	for i := 1; i < len(m.modalQueue); i++ {
		if p := modalPriorityOf(m.modalQueue[i]); p < bestPrio {
			bestPrio = p
			bestIdx = i
		}
	}
	mod := m.modalQueue[bestIdx]
	m.modalQueue = append(m.modalQueue[:bestIdx], m.modalQueue[bestIdx+1:]...)
	if len(m.modalQueue) == 0 {
		m.modalQueue = nil
	}
	// Refresh parked setup wizard with live provider/model before showing it.
	if f, ok := mod.(*ftueModal); ok {
		f.syncFrom(m.providerName, m.modelName)
	}
	// Refresh parked tour with live surface facts (windows, keybinds, modes).
	if t, ok := mod.(*tourModal); ok {
		t.ctx = m.buildTourContext()
		// Keep section list stable for an in-progress visit; only rebuild when empty.
		if len(t.sections) == 0 {
			t.sections = buildTourSections(t.ctx)
		}
	}
	m.modal = mod
	return m.armVisibleBlocking(mod)
}

// afterModalClosed promotes the next queued modal when the top slot is empty.
// Call after any path that may have set m.modal = nil without replacing it.
func (m *Model) afterModalClosed() tea.Cmd {
	if m.modal != nil {
		return nil
	}
	return m.promoteModalQueue()
}

// clearModalStack drops the visible modal and every queued ask (session switch,
// root load that should not carry overlays, quit paths).
func (m *Model) clearModalStack() {
	if pm, ok := m.modal.(*permissionModal); ok {
		pm.cancelCountdown()
	}
	m.modal = nil
	m.modalQueue = nil
}

// pendingBlockingCount is the number of permission/question asks waiting
// behind the visible modal (not including the top slot).
func (m Model) pendingBlockingCount() int {
	n := 0
	for _, q := range m.modalQueue {
		switch q.(type) {
		case *permissionModal, *questionModal:
			n++
		}
	}
	return n
}

// pendingBlockingLabel is a short badge body like "1 permission waiting".
func (m Model) pendingBlockingLabel() string {
	n := m.pendingBlockingCount()
	if n <= 0 {
		return ""
	}
	perms, questions := 0, 0
	for _, q := range m.modalQueue {
		switch q.(type) {
		case *permissionModal:
			perms++
		case *questionModal:
			questions++
		}
	}
	switch {
	case perms > 0 && questions == 0:
		if perms == 1 {
			return "1 permission waiting"
		}
		return itoa(perms) + " permissions waiting"
	case questions > 0 && perms == 0:
		if questions == 1 {
			return "1 question waiting"
		}
		return itoa(questions) + " questions waiting"
	default:
		if n == 1 {
			return "1 ask waiting"
		}
		return itoa(n) + " asks waiting"
	}
}

// refreshAwaitingPermission keeps attention state true while any ask is
// visible or queued (multi-ask must not clear on the first resolve).
func (m *Model) refreshAwaitingPermission() {
	if _, ok := m.modal.(*permissionModal); ok {
		m.awaitingPermission = true
		return
	}
	if _, ok := m.modal.(*questionModal); ok {
		m.awaitingPermission = true
		return
	}
	for _, q := range m.modalQueue {
		switch q.(type) {
		case *permissionModal, *questionModal:
			m.awaitingPermission = true
			return
		}
	}
	m.awaitingPermission = false
}

// cloneModalQueue returns a shallow copy of the queue slice (modals are pointers).
func cloneModalQueue(q []modal) []modal {
	if len(q) == 0 {
		return nil
	}
	return append([]modal(nil), q...)
}
