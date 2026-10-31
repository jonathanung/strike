package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/protocol"
)

type fakeOnboarding struct {
	acknowledged bool
	ackErr       error
	acks         int
}

func (f *fakeOnboarding) ShouldAutoOpen() bool { return !f.acknowledged }
func (f *fakeOnboarding) Acknowledged() bool   { return f.acknowledged }
func (f *fakeOnboarding) Acknowledge() error {
	f.acks++
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acknowledged = true
	return nil
}

func onboardingServer(t *testing.T, ob host.Onboarding, live bool) *Server {
	t.Helper()
	opts := Options{SessionDir: t.TempDir()}
	if ob != nil {
		opts.Services = &host.Services{Onboarding: ob}
	}
	if live {
		opts.Live = NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func getOnboarding(t *testing.T, srv *Server) onboardingResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/onboarding", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/onboarding = %d %s", rec.Code, rec.Body.String())
	}
	var doc onboardingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestOnboardingEndpointsRoundTripAcrossBrowsers(t *testing.T) {
	// One daemon record shared by every "browser": each request below is a
	// fresh client with no localStorage of its own (#1552).
	ob := &fakeOnboarding{}
	srv := onboardingServer(t, ob, true)

	if doc := getOnboarding(t, srv); doc.Acknowledged {
		t.Fatal("fresh daemon must report acknowledged=false")
	}

	ack := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/v1/onboarding/acknowledge", nil))
	if ack.Code != http.StatusOK || !strings.Contains(ack.Body.String(), `"acknowledged":true`) {
		t.Fatalf("acknowledge = %d %s", ack.Code, ack.Body.String())
	}
	if ob.acks != 1 {
		t.Fatalf("acks = %d, want 1", ob.acks)
	}

	// A second browser (a brand-new request, no shared state) now sees the
	// daemon record and skips the card.
	if doc := getOnboarding(t, srv); !doc.Acknowledged {
		t.Fatal("second browser must see acknowledged=true after the first browser acknowledged")
	}

	// Idempotent: acknowledging again stays acknowledged.
	ack2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ack2, httptest.NewRequest(http.MethodPost, "/v1/onboarding/acknowledge", nil))
	if ack2.Code != http.StatusOK {
		t.Fatalf("second acknowledge = %d %s", ack2.Code, ack2.Body.String())
	}
}

func TestOnboardingUnavailableWithoutService(t *testing.T) {
	srv := onboardingServer(t, nil, true)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/onboarding", nil),
		httptest.NewRequest(http.MethodPost, "/v1/onboarding/acknowledge", nil),
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "onboarding capability unavailable") {
			t.Fatalf("%s %s = %d %s, want 501 onboarding unavailable", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

func TestOnboardingAcknowledgeRejectedInAttachOnly(t *testing.T) {
	ob := &fakeOnboarding{}
	srv := onboardingServer(t, ob, false)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/onboarding/acknowledge", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("attach-only acknowledge = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if ob.acks != 0 {
		t.Fatalf("attach-only acknowledge wrote the record: acks = %d", ob.acks)
	}
	// Reads still work in attach-only mode.
	if doc := getOnboarding(t, srv); doc.Acknowledged {
		t.Fatal("attach-only GET must report the untouched record")
	}
}

func TestOnboardingAcknowledgeErrorSurfaces(t *testing.T) {
	ob := &fakeOnboarding{ackErr: errors.New("disk full")}
	srv := onboardingServer(t, ob, true)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/onboarding/acknowledge", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "disk full") {
		t.Fatalf("acknowledge error = %d %s, want 500 with the cause", rec.Code, rec.Body.String())
	}
}
