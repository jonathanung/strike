package powers

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// gateConn blocks Connect for servers in gates until the gate is closed.
// It is goroutine-safe so the race detector can check the manager.
type gateConn struct {
	mu     sync.Mutex
	gates  map[string]chan struct{}
	fail   map[string]bool
	calls  map[string]int // server -> Connect calls
	live   map[string]bool
	closed []string
}

func newGateConn() *gateConn {
	return &gateConn{gates: map[string]chan struct{}{}, fail: map[string]bool{}, calls: map[string]int{}, live: map[string]bool{}}
}

func (g *gateConn) Connect(session, server string) error {
	g.mu.Lock()
	g.calls[server]++
	gate := g.gates[server]
	g.mu.Unlock()
	if gate != nil {
		<-gate
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail[server] {
		return errors.New("dial " + server + ": refused")
	}
	g.live[session+"/"+server] = true
	return nil
}

func (g *gateConn) Disconnect(session, server string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.live, session+"/"+server)
	g.closed = append(g.closed, session+"/"+server)
}

func (g *gateConn) snapshot() (calls map[string]int, live map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	calls = map[string]int{}
	for k, v := range g.calls {
		calls[k] = v
	}
	live = map[string]bool{}
	for k, v := range g.live {
		live[k] = v
	}
	return calls, live
}

// waitCalls polls until server has been dialed n times.
func waitCalls(t *testing.T, g *gateConn, server string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		calls, _ := g.snapshot()
		if calls[server] >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d dials of %s", n, server)
}

func TestSlowDialDoesNotStallOtherSessions(t *testing.T) {
	g := newGateConn()
	gate := make(chan struct{})
	g.gates["slow"] = gate
	m := NewManager(g, nil)

	done := make(chan error, 1)
	go func() {
		_, err := m.Activate("s1", Power{Name: "slow-pack", Servers: []string{"slow"}})
		done <- err
	}()
	waitCalls(t, g, "slow", 1)

	// While s1 is mid-dial, s2 activates and s1 can still be read.
	fast := make(chan error, 1)
	go func() {
		_, err := m.Activate("s2", Power{Name: "fast", Servers: []string{"quick"}})
		fast <- err
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("s2 activation stalled behind s1's dial")
	}
	if got := m.Active("s1"); got != nil {
		t.Fatalf("pending pack must not show as active yet: %+v", got)
	}
	if _, err := m.Activate("s1", Power{Name: "slow-pack"}); err == nil {
		t.Fatal("activating a pack that is already in flight should fail")
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := m.Servers("s1"); !reflect.DeepEqual(got, []string{"slow"}) {
		t.Fatalf("s1 servers = %v", got)
	}
}

func TestConcurrentActivationsShareOneDial(t *testing.T) {
	g := newGateConn()
	gate := make(chan struct{})
	g.gates["shared"] = gate
	m := NewManager(g, nil)

	errs := make(chan error, 2)
	go func() {
		_, err := m.Activate("s1", Power{Name: "a", Servers: []string{"shared"}})
		errs <- err
	}()
	waitCalls(t, g, "shared", 1)
	go func() {
		_, err := m.Activate("s1", Power{Name: "b", Servers: []string{"shared"}})
		errs <- err
	}()
	// Let b register as a waiter on a's dial before releasing it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		w := 0
		if st := m.sessions["s1"]; st != nil && st.dials["shared"] != nil {
			w = st.dials["shared"].waiters
		}
		m.mu.Unlock()
		if w == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b never joined a's dial")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if calls, _ := g.snapshot(); calls["shared"] != 1 {
		t.Fatalf("shared dialed %d times, want 1", calls["shared"])
	}
	// The shared server stays until the last pack is deactivated.
	if err := m.Deactivate("s1", "a"); err != nil {
		t.Fatal(err)
	}
	if _, live := g.snapshot(); !live["s1/shared"] {
		t.Fatal("shared server dropped while b still holds it")
	}
	if err := m.Deactivate("s1", "b"); err != nil {
		t.Fatal(err)
	}
	if _, live := g.snapshot(); live["s1/shared"] {
		t.Fatal("shared server should be disconnected after the last pack")
	}
}

func TestSharedDialFailureReachesWaiter(t *testing.T) {
	g := newGateConn()
	gate := make(chan struct{})
	g.gates["bad"] = gate
	g.fail["bad"] = true
	m := NewManager(g, nil)

	type out struct {
		res Result
		err error
	}
	results := make(chan out, 2)
	go func() {
		r, err := m.Activate("s1", Power{Name: "a", Servers: []string{"bad"}})
		results <- out{r, err}
	}()
	waitCalls(t, g, "bad", 1)
	go func() {
		r, err := m.Activate("s1", Power{Name: "b", Servers: []string{"bad", "good"}})
		results <- out{r, err}
	}()
	waitCalls(t, g, "good", 1)
	close(gate)
	for i := 0; i < 2; i++ {
		o := <-results
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.res.Failed["bad"] == "" {
			t.Fatalf("%s: failure not reported: %+v", o.res.Name, o.res)
		}
	}
	if got := m.Servers("s1"); !reflect.DeepEqual(got, []string{"good"}) {
		t.Fatalf("tool set = %v, want only good", got)
	}
}

func TestEndSessionDuringDialReleasesServer(t *testing.T) {
	g := newGateConn()
	gate := make(chan struct{})
	g.gates["slow"] = gate
	m := NewManager(g, nil)

	done := make(chan error, 1)
	go func() {
		_, err := m.Activate("s1", Power{Name: "p", Servers: []string{"slow"}})
		done <- err
	}()
	waitCalls(t, g, "slow", 1)
	m.EndSession("s1")
	if _, err := m.Activate("s1", Power{Name: "q"}); err == nil {
		t.Fatal("activation on an ended session should fail")
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("activation finishing after EndSession should fail")
	}
	if _, live := g.snapshot(); live["s1/slow"] {
		t.Fatal("server connected for an ended session must be disconnected")
	}
	m.mu.Lock()
	_, kept := m.sessions["s1"]
	m.mu.Unlock()
	if kept {
		t.Fatal("ended session state should be dropped once idle")
	}
}

func TestLastUsed(t *testing.T) {
	m := NewManager(newGateConn(), nil)
	at := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	if _, ok := m.LastUsed("p"); ok {
		t.Fatal("unused pack should have no last-used time")
	}
	if _, err := m.Activate("s", Power{Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.LastUsed("p"); !ok || !got.Equal(at) {
		t.Fatalf("LastUsed = %v %v", got, ok)
	}
}

func TestServerConfigs(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "p", `{"name":"p"}`, `{"mcpServers":{"a":{"command":"x"}}}`)
	writePack(t, root, "q", `{"name":"q"}`, "")
	p, err := LoadDir(root+"/p", ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := ServerConfigs(p)
	if err != nil || string(cfgs["a"]) != `{"command":"x"}` {
		t.Fatalf("ServerConfigs = %v %v", cfgs, err)
	}
	q, err := LoadDir(root+"/q", ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if cfgs, err := ServerConfigs(q); err != nil || cfgs != nil {
		t.Fatalf("pack without mcp.json = %v %v", cfgs, err)
	}
}
