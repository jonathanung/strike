package trigger

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/pkg/redact"
)

// Adapter names an inbound message transport. Webhook and GitHub are HTTP
// receivers in this package. A chat adapter owns its own connection and calls
// Service.Accept; this process does not dial one.
type Adapter interface {
	Name() string
}

// Message is one inbound channel payload. MessageID dedupes per trigger.
type Message struct {
	Adapter   string
	Channel   string
	MessageID string
	Sender    string
	Text      string
}

// Hook is one HTTP delivery. Synthetic is the authenticated test path and
// skips HMAC. The raw body is what was signed.
type Hook struct {
	ID         string
	Body       []byte
	Timestamp  string
	DeliveryID string
	Signature  string
	Event      string
	Synthetic  bool
	// Text, Sender, and Channel carry a synthetic channel message.
	Text    string
	Sender  string
	Channel string
}

// Result is the HTTP-facing outcome. Error is a short reason with no secret.
type Result struct {
	Status    int    `json:"-"`
	OK        bool   `json:"ok"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"sessionId,omitempty"`
	Error     string `json:"error,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
	// pending is in-memory only. It is unexported so it cannot leak on the wire.
	pending pending
}

type mem struct {
	seen   map[string]struct{}
	prints map[string]time.Time
	hits   []time.Time
	fails  []time.Time
	noted  time.Time
	loaded bool
}

// Service is the trigger registry plus delivery decisions.
type Service struct {
	store    *Store
	launch   rootstart.Launcher
	now      func() time.Time
	mu       sync.Mutex
	state    map[string]*mem
	adapters map[string]Adapter
}

// New binds a store to the shared root launcher. A nil launcher rejects
// deliveries that would start a session and still records the error.
func New(store *Store, launch rootstart.Launcher) *Service {
	return &Service{
		store:    store,
		launch:   launch,
		now:      time.Now,
		state:    map[string]*mem{},
		adapters: map[string]Adapter{},
	}
}

// SetClock overrides the clock. Tests use it.
func (s *Service) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.now = now
	s.store.now = now
	s.mu.Unlock()
}

// Register records an adapter name. A second registration of the same name
// replaces the previous value.
func (s *Service) Register(a Adapter) error {
	if a == nil || strings.TrimSpace(a.Name()) == "" {
		return errMalformed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters[a.Name()] = a
	return nil
}

// Adapters lists registered channel adapter names.
func (s *Service) Adapters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.adapters))
	for name := range s.adapters {
		out = append(out, name)
	}
	return out
}

func (s *Service) List() []View {
	return s.store.List()
}

func (s *Service) Create(in Input) (View, string, error) {
	return s.store.Create(in)
}

func (s *Service) Update(id string, in Input) (View, string, error) {
	if err := validateID(id); err != nil {
		return View{}, "", err
	}
	return s.store.Update(id, in)
}

func (s *Service) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Delete(id); err != nil {
		return err
	}
	delete(s.state, id)
	return nil
}

func (s *Service) Deliveries(id string) ([]DeliveryView, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	if _, err := s.store.Get(id); err != nil {
		return nil, err
	}
	rows := s.store.Deliveries(id)
	out := make([]DeliveryView, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].view())
	}
	return out, nil
}

// Receive handles an inbound HTTP hook.
func (s *Service) Receive(ctx context.Context, hook Hook) Result {
	if len(hook.Body) > MaxBody {
		return Result{Status: 413, Outcome: OutcomeRejected, Error: "payload too large"}
	}
	s.mu.Lock()
	res, req := s.decide(hook)
	s.mu.Unlock()
	if req == nil {
		return res
	}
	return s.finish(ctx, hook.ID, res, *req)
}

// Accept delivers one channel message to every enabled trigger bound to it.
func (s *Service) Accept(ctx context.Context, msg Message) ([]Result, error) {
	msg.Adapter = strings.TrimSpace(msg.Adapter)
	msg.Channel = strings.TrimSpace(msg.Channel)
	msg.MessageID = strings.TrimSpace(msg.MessageID)
	msg.Sender = strings.TrimSpace(msg.Sender)
	msg.Text = strings.TrimSpace(msg.Text)
	if msg.Adapter == "" || msg.MessageID == "" || msg.Text == "" {
		return nil, errMalformed
	}
	if len(msg.MessageID) > 128 || strings.ContainsAny(msg.MessageID, " \n\r/\\") || len(msg.Text) > MaxBody {
		return nil, errMalformed
	}
	var matched []record
	for _, view := range s.store.List() {
		if view.Kind != KindChannel || !view.Enabled || view.Adapter != msg.Adapter {
			continue
		}
		if view.Channel != "" && view.Channel != msg.Channel {
			continue
		}
		rec, err := s.store.Get(view.ID)
		if err != nil {
			continue
		}
		matched = append(matched, rec)
	}
	out := make([]Result, 0, len(matched))
	for _, rec := range matched {
		hook := Hook{
			ID:         rec.ID,
			DeliveryID: msg.MessageID,
			Event:      "channel",
			Synthetic:  true,
		}
		s.mu.Lock()
		res, req := s.decideChannel(rec, msg, hook)
		s.mu.Unlock()
		if req == nil {
			out = append(out, res)
			continue
		}
		out = append(out, s.finish(ctx, rec.ID, res, *req))
	}
	return out, nil
}

// Test is the authenticated synthetic delivery. It skips HMAC and still
// applies rate limits, dedupe, and the permission posture.
func (s *Service) Test(ctx context.Context, id string, hook Hook) Result {
	hook.ID = id
	hook.Synthetic = true
	if strings.TrimSpace(hook.DeliveryID) == "" {
		hook.DeliveryID = "test-" + newID()
	}
	return s.Receive(ctx, hook)
}

// Replay starts another session from a stored redacted prompt.
func (s *Service) Replay(ctx context.Context, id, deliveryID string) Result {
	if err := validateID(id); err != nil {
		return Result{Status: 400, Outcome: OutcomeError, Error: err.Error()}
	}
	rec, err := s.store.Get(id)
	if err != nil {
		return Result{Status: 404, Outcome: OutcomeError, Error: "trigger not found"}
	}
	var prior *Delivery
	for _, row := range s.store.Deliveries(id) {
		if row.ID == deliveryID {
			copy := row
			prior = &copy
		}
	}
	if prior == nil || strings.TrimSpace(prior.Prompt) == "" || (prior.Outcome != OutcomeAccepted && prior.Outcome != OutcomeReplayed) {
		return Result{Status: 404, Outcome: OutcomeError, Error: "nothing to replay"}
	}
	now := s.now()
	s.mu.Lock()
	st := s.ensure(id)
	if res, limited := s.hold(id, st, rec.RatePerMinute, now, prior.Event); limited {
		s.mu.Unlock()
		return res
	}
	st.hits = append(prune(st.hits, now), now)
	req := s.request(rec, prior.Prompt, "replay "+prior.Event)
	res := Result{Status: 200, OK: true, Outcome: OutcomeReplayed}
	delivID := "replay-" + newID()
	s.mu.Unlock()
	started, err := s.start(ctx, req)
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Delivery{
		ID:      delivID,
		At:      s.now().UTC(),
		Outcome: OutcomeReplayed,
		Event:   prior.Event,
		Action:  prior.Action,
		Repo:    prior.Repo,
		Prompt:  prior.Prompt,
		Reason:  "replay",
	}
	if err != nil {
		d.Outcome = OutcomeError
		d.Reason = err.Error()
		d.Prompt = ""
		s.log(id, s.ensure(id), d, false)
		return Result{Status: 502, Outcome: OutcomeError, Error: "session did not start"}
	}
	d.SessionID = started.SessionID
	s.log(id, s.ensure(id), d, true)
	res.SessionID = started.SessionID
	return res
}

func (s *Service) decide(hook Hook) (Result, *rootstart.Request) {
	if err := validateID(hook.ID); err != nil {
		return Result{Status: 404, Outcome: OutcomeError, Error: "trigger not found"}, nil
	}
	rec, err := s.store.Get(hook.ID)
	if err != nil {
		return Result{Status: 404, Outcome: OutcomeError, Error: "trigger not found"}, nil
	}
	if rec.Kind == KindChannel {
		if !hook.Synthetic {
			return s.reject(rec, hook, "signature rejected", 401, OutcomeRejected), nil
		}
		deliveryID := strings.TrimSpace(hook.DeliveryID)
		if deliveryID == "" || len(deliveryID) > 128 || strings.ContainsAny(deliveryID, " \n\r") {
			return s.reject(rec, hook, "delivery id is required", 400, OutcomeRejected), nil
		}
		text := strings.TrimSpace(hook.Text)
		if text == "" {
			text = strings.TrimSpace(string(hook.Body))
		}
		if text == "" {
			return s.reject(rec, hook, "message text is required", 400, OutcomeRejected), nil
		}
		if !rec.Enabled {
			now := s.now()
			st := s.ensure(rec.ID)
			if _, ok := st.seen[deliveryID]; ok {
				s.log(rec.ID, st, Delivery{
					ID:      deliveryID,
					At:      now.UTC(),
					Outcome: OutcomeDuplicate,
					Event:   "channel",
					Reason:  "duplicate delivery",
				}, true)
				return Result{Status: 200, OK: true, Outcome: OutcomeDuplicate, Duplicate: true}, nil
			}
			if s.limited(st.hits, rec.RatePerMinute, now) {
				return Result{Status: 429, Outcome: OutcomeRateLimited, Error: "rate limited"}, nil
			}
			st.hits = append(prune(st.hits, now), now)
			return s.reject(rec, hook, "trigger disabled", 403, OutcomeDisabled), nil
		}
		channel := strings.TrimSpace(hook.Channel)
		if channel == "" {
			channel = rec.Channel
		}
		return s.decideChannel(rec, Message{
			Adapter:   rec.Adapter,
			Channel:   channel,
			MessageID: deliveryID,
			Sender:    hook.Sender,
			Text:      text,
		}, hook)
	}
	now := s.now()
	st := s.ensure(rec.ID)
	if !hook.Synthetic {
		var verr error
		switch rec.Kind {
		case KindGitHub:
			verr = verifyGitHub(rec.Secret, hook.Body, hook.Signature)
		default:
			verr = verifyWebhook(rec.Secret, hook.Timestamp, hook.Body, hook.Signature, now)
		}
		if verr != nil {
			// Past the failure budget, stay quiet. Logging every miss would
			// let an unsigned flood fill the delivery log.
			if s.limited(st.fails, failBudget(rec.RatePerMinute), now) {
				return Result{Status: 429, Outcome: OutcomeRateLimited, Error: "rate limited"}, nil
			}
			st.fails = append(prune(st.fails, now), now)
			status := 401
			reason := "signature rejected"
			if verr == errStale {
				reason = "timestamp outside the replay window"
			} else if verr == errMalformed {
				reason = "malformed signature headers"
			}
			return s.reject(rec, hook, reason, status, OutcomeRejected), nil
		}
	}
	deliveryID := strings.TrimSpace(hook.DeliveryID)
	if deliveryID == "" || len(deliveryID) > 128 || strings.ContainsAny(deliveryID, " \n\r") {
		return s.reject(rec, hook, "delivery id is required", 400, OutcomeRejected), nil
	}
	if _, ok := st.seen[deliveryID]; ok {
		s.log(rec.ID, st, Delivery{
			ID:      deliveryID,
			At:      now.UTC(),
			Outcome: OutcomeDuplicate,
			Event:   hook.Event,
			Reason:  "duplicate delivery",
		}, true)
		return Result{Status: 200, OK: true, Outcome: OutcomeDuplicate, Duplicate: true}, nil
	}
	if !rec.Enabled {
		if s.limited(st.hits, rec.RatePerMinute, now) {
			return Result{Status: 429, Outcome: OutcomeRateLimited, Error: "rate limited"}, nil
		}
		st.hits = append(prune(st.hits, now), now)
		return s.reject(rec, hook, "trigger disabled", 403, OutcomeDisabled), nil
	}
	var parsed eventFields
	switch rec.Kind {
	case KindGitHub:
		parsed, err = githubEvent(rec.Secret, hook.Event, hook.Body, rec.Events)
	default:
		parsed = webhookEvent(rec.Secret, hook.Event, hook.Body)
	}
	if err != nil {
		return s.reject(rec, hook, err.Error(), 400, OutcomeRejected), nil
	}
	if parsed.ignore != "" {
		s.log(rec.ID, st, Delivery{
			ID:      deliveryID,
			At:      now.UTC(),
			Outcome: OutcomeIgnored,
			Event:   hook.Event,
			Reason:  parsed.ignore,
		}, true)
		return Result{Status: 200, OK: true, Outcome: OutcomeIgnored}, nil
	}
	if rec.DedupeSeconds > 0 && parsed.fingerprint != "" {
		if at, ok := st.prints[parsed.fingerprint]; ok && now.Sub(at) < time.Duration(rec.DedupeSeconds)*time.Second {
			s.log(rec.ID, st, Delivery{
				ID:          deliveryID,
				At:          now.UTC(),
				Outcome:     OutcomeDuplicate,
				Event:       parsed.fields["event"],
				Fingerprint: parsed.fingerprint,
				Reason:      "duplicate event",
			}, true)
			return Result{Status: 200, OK: true, Outcome: OutcomeDuplicate, Duplicate: true}, nil
		}
	}
	if res, limited := s.hold(rec.ID, st, rec.RatePerMinute, now, parsed.fields["event"]); limited {
		return res, nil
	}
	st.hits = append(prune(st.hits, now), now)
	st.seen[deliveryID] = struct{}{}
	prompt := scrub(rec.Secret, renderPrompt(rec.Kind, rec.Prompt, parsed.fields))
	req := s.request(rec, prompt, parsed.fields["event"])
	return Result{
		Status:  202,
		OK:      true,
		Outcome: OutcomeAccepted,
		pending: pending{
			deliveryID:  deliveryID,
			event:       parsed.fields["event"],
			action:      parsed.fields["action"],
			repo:        parsed.fields["repo"],
			fingerprint: parsed.fingerprint,
			prompt:      prompt,
		},
	}, &req
}

func (s *Service) decideChannel(rec record, msg Message, hook Hook) (Result, *rootstart.Request) {
	now := s.now()
	st := s.ensure(rec.ID)
	if _, ok := st.seen[hook.DeliveryID]; ok {
		s.log(rec.ID, st, Delivery{
			ID:      hook.DeliveryID,
			At:      now.UTC(),
			Outcome: OutcomeDuplicate,
			Event:   "channel",
			Reason:  "duplicate delivery",
		}, true)
		return Result{Status: 200, OK: true, Outcome: OutcomeDuplicate, Duplicate: true}, nil
	}
	parsed := channelEvent(rec.Secret, msg)
	if rec.DedupeSeconds > 0 {
		if at, ok := st.prints[parsed.fingerprint]; ok && now.Sub(at) < time.Duration(rec.DedupeSeconds)*time.Second {
			s.log(rec.ID, st, Delivery{
				ID:          hook.DeliveryID,
				At:          now.UTC(),
				Outcome:     OutcomeDuplicate,
				Event:       "channel",
				Fingerprint: parsed.fingerprint,
				Reason:      "duplicate event",
			}, true)
			return Result{Status: 200, OK: true, Outcome: OutcomeDuplicate, Duplicate: true}, nil
		}
	}
	if res, limited := s.hold(rec.ID, st, rec.RatePerMinute, now, "channel"); limited {
		return res, nil
	}
	st.hits = append(prune(st.hits, now), now)
	st.seen[hook.DeliveryID] = struct{}{}
	prompt := scrub(rec.Secret, renderPrompt(rec.Kind, rec.Prompt, parsed.fields))
	req := s.request(rec, prompt, "channel")
	return Result{
		Status:  202,
		OK:      true,
		Outcome: OutcomeAccepted,
		pending: pending{
			deliveryID:  hook.DeliveryID,
			event:       "channel",
			fingerprint: parsed.fingerprint,
			prompt:      prompt,
		},
	}, &req
}

type pending struct {
	deliveryID  string
	event       string
	action      string
	repo        string
	fingerprint string
	prompt      string
}

func (s *Service) finish(ctx context.Context, id string, res Result, req rootstart.Request) Result {
	started, err := s.start(ctx, req)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(id)
	d := Delivery{
		ID:          res.pending.deliveryID,
		At:          s.now().UTC(),
		Outcome:     OutcomeAccepted,
		Event:       res.pending.event,
		Action:      res.pending.action,
		Repo:        res.pending.repo,
		Fingerprint: res.pending.fingerprint,
		Prompt:      res.pending.prompt,
	}
	if err != nil {
		delete(st.seen, res.pending.deliveryID)
		d.Outcome = OutcomeError
		d.Reason = "session did not start"
		d.Prompt = ""
		s.log(id, st, d, false)
		return Result{Status: 502, Outcome: OutcomeError, Error: "session did not start"}
	}
	d.SessionID = started.SessionID
	if res.pending.fingerprint != "" {
		st.prints[res.pending.fingerprint] = d.At
	}
	s.log(id, st, d, true)
	res.SessionID = started.SessionID
	res.pending = pending{}
	return res
}

func (s *Service) start(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	if s.launch == nil {
		return rootstart.Result{}, errNotFound
	}
	return s.launch.StartRoot(ctx, req)
}

func (s *Service) request(rec record, prompt, event string) rootstart.Request {
	title := rec.Name
	if event != "" {
		title = rec.Name + " · " + event
	}
	return rootstart.Request{
		Source:         rootstart.SourceTrigger,
		SourceID:       rec.ID,
		Prompt:         prompt,
		PermissionMode: rec.PermissionMode,
		ContinueID:     rec.ContinueSessionID,
		Title:          truncate(title, 80),
	}
}

func (s *Service) reject(rec record, hook Hook, reason string, status int, outcome string) Result {
	st := s.ensure(rec.ID)
	id := strings.TrimSpace(hook.DeliveryID)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, " \n\r") {
		id = "reject-" + newID()
	}
	// Rejects do not reserve the delivery id. An unsigned request must not
	// be able to claim an id and drop the real delivery that follows.
	s.log(rec.ID, st, Delivery{
		ID:      id,
		At:      s.now().UTC(),
		Outcome: outcome,
		Event:   scrub(rec.Secret, hook.Event),
		Reason:  reason,
	}, false)
	return Result{Status: status, Outcome: outcome, Error: reason}
}

func (s *Service) log(id string, st *mem, d Delivery, seal bool) {
	d.Reason = truncate(redact.String(d.Reason), 200)
	d.Event = truncate(redact.String(d.Event), 80)
	d.Repo = truncate(redact.String(d.Repo), 200)
	d.Action = truncate(redact.String(d.Action), 80)
	d.Prompt = redact.String(d.Prompt)
	if seal && d.ID != "" {
		st.seen[d.ID] = struct{}{}
	}
	_ = s.store.AppendDelivery(id, d)
}

func (s *Service) ensure(id string) *mem {
	st := s.state[id]
	if st != nil && st.loaded {
		return st
	}
	if st == nil {
		st = &mem{seen: map[string]struct{}{}, prints: map[string]time.Time{}}
		s.state[id] = st
	}
	for _, row := range s.store.Deliveries(id) {
		if seals(row.Outcome) && row.ID != "" {
			st.seen[row.ID] = struct{}{}
		}
		if row.Outcome == OutcomeAccepted && row.Fingerprint != "" {
			st.prints[row.Fingerprint] = row.At
		}
	}
	st.loaded = true
	return st
}

func seals(outcome string) bool {
	switch outcome {
	case OutcomeAccepted, OutcomeDuplicate, OutcomeIgnored, OutcomeReplayed:
		return true
	default:
		return false
	}
}

// hold reports a rate limit without recording another hit. The first
// limit in a minute is written to the log; later ones only return 429.
func (s *Service) hold(id string, st *mem, budget int, now time.Time, event string) (Result, bool) {
	if !s.limited(st.hits, budget, now) {
		return Result{}, false
	}
	if st.noted.IsZero() || now.Sub(st.noted) >= time.Minute {
		st.noted = now
		s.log(id, st, Delivery{
			ID:      "rate-" + newID(),
			At:      now.UTC(),
			Outcome: OutcomeRateLimited,
			Event:   event,
			Reason:  "rate limited",
		}, false)
	}
	return Result{Status: 429, Outcome: OutcomeRateLimited, Error: "rate limited"}, true
}

func (s *Service) limited(hits []time.Time, budget int, now time.Time) bool {
	n := 0
	for _, at := range hits {
		if now.Sub(at) < time.Minute {
			n++
		}
	}
	return n >= budget
}

func prune(hits []time.Time, now time.Time) []time.Time {
	out := hits[:0]
	for _, at := range hits {
		if now.Sub(at) < time.Minute {
			out = append(out, at)
		}
	}
	return out
}

func failBudget(rate int) int {
	n := rate * 5
	if n < 30 {
		n = 30
	}
	return n
}
