package server

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/audit"
)

// Default ops rate limits (per client IP). Interactive cockpit use stays under
// these; automated floods get 429 / WS error frames.
const (
	defaultOpsRate  = 10.0 // tokens per second
	defaultOpsBurst = 20
)

// OpAuditor records serve control-plane ops (type + source IP only).
// *audit.Sink satisfies this; nil is a no-op.
type OpAuditor interface {
	Record(family string, sessionID, turnID, toolCallID, chainID string, payload any) error
}

// ServeOpPayload is the audit payload for family serve_op (no op body).
type ServeOpPayload struct {
	OpType   string `json:"opType" telemetry:"redact=none"`
	SourceIP string `json:"sourceIp" telemetry:"redact=none"`
	Channel  string `json:"channel" telemetry:"redact=none"` // http|ws
	Outcome  string `json:"outcome" telemetry:"redact=none"` // ok|rate_limited|read_only|error
}

const (
	opOutcomeOK          = "ok"
	opOutcomeRateLimited = "rate_limited"
	opOutcomeReadOnly    = "read_only"
	opOutcomeError       = "error"
)

var (
	errOpsRateLimited = errors.New("ops rate limited")
	errOpsReadOnly    = errors.New("server is read-only; mutating ops rejected")
)

// opLimiter is a per-IP token bucket.
type opLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	clock   func() time.Time
	buckets map[string]*opBucket
}

type opBucket struct {
	tokens float64
	last   time.Time
}

func newOpLimiter(rate float64, burst int, clock func() time.Time) *opLimiter {
	if rate <= 0 {
		rate = defaultOpsRate
	}
	if burst <= 0 {
		burst = defaultOpsBurst
	}
	if clock == nil {
		clock = time.Now
	}
	return &opLimiter{
		rate:    rate,
		burst:   float64(burst),
		clock:   clock,
		buckets: make(map[string]*opBucket),
	}
}

func (l *opLimiter) allow(ip string) bool {
	if l == nil {
		return true
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[ip]
	if b == nil {
		b = &opBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Server) clientIPString(r *http.Request) string {
	ip := ClientIP(r.RemoteAddr)
	if ip == nil {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return strings.TrimSpace(r.RemoteAddr)
		}
		return host
	}
	return ip.String()
}

// admitOp enforces read-only + rate limit. Denials are audited; callers audit
// ok/error after Submit. sessionID may be empty when unknown.
func (s *Server) admitOp(r *http.Request, channel, opType, sessionID string) error {
	ip := s.clientIPString(r)
	opType = strings.TrimSpace(opType)
	if opType == "" {
		opType = "unknown"
	}

	if s.opts.ReadOnly {
		s.recordServeOp(sessionID, opType, ip, channel, opOutcomeReadOnly)
		return errOpsReadOnly
	}
	if s.opsLimit != nil && !s.opsLimit.allow(ip) {
		s.recordServeOp(sessionID, opType, ip, channel, opOutcomeRateLimited)
		return errOpsRateLimited
	}
	return nil
}

func (s *Server) auditOpOK(r *http.Request, channel, opType, sessionID string) {
	s.recordServeOp(sessionID, opType, s.clientIPString(r), channel, opOutcomeOK)
}

func (s *Server) recordServeOp(sessionID, opType, ip, channel, outcome string) {
	if s == nil || s.opts.Audit == nil {
		return
	}
	_ = s.opts.Audit.Record(audit.FamilyServeOp, sessionID, "", "", "", ServeOpPayload{
		OpType:   opType,
		SourceIP: ip,
		Channel:  channel,
		Outcome:  outcome,
	})
}

func (s *Server) recordServeOpError(r *http.Request, channel, opType, sessionID string) {
	if s == nil {
		return
	}
	s.recordServeOp(sessionID, opType, s.clientIPString(r), channel, opOutcomeError)
}
