package trigger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestProblemReportedWhenSessionDoesNotStart(t *testing.T) {
	cases := []struct {
		name      string
		launchErr error
		wantCount int
	}{
		{"session starts: no problem", nil, 0},
		{"session fails: one problem", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, launch := newSvc(t)
			launch.err = tc.launchErr
			var mu sync.Mutex
			var got []Problem
			svc.SetProblems(func(p Problem) {
				mu.Lock()
				got = append(got, p)
				mu.Unlock()
			})
			view, secret, err := svc.Create(Input{Name: "nightly deploy", Kind: KindWebhook, Prompt: "deploy it"})
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"note":"` + secret + `"}`)
			ts := unix(svc.now())
			svc.Receive(context.Background(), Hook{
				ID: view.ID, Body: body, Timestamp: ts, DeliveryID: "d1",
				Signature: SignWebhook(secret, ts, body), Event: "deploy",
			})
			svc.Wait()
			mu.Lock()
			defer mu.Unlock()
			if len(got) != tc.wantCount {
				t.Fatalf("problems = %+v, want %d", got, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			p := got[0]
			if p.TriggerID != view.ID || p.TriggerName != "nightly deploy" || p.Event != "deploy" || p.DeliveryID != "d1" {
				t.Fatalf("problem = %+v", p)
			}
			if strings.Contains(p.TriggerName+p.Event+p.DeliveryID, secret) {
				t.Fatal("problem carries the secret")
			}
		})
	}
}

func TestNoProblemReceiverIsFine(t *testing.T) {
	svc, launch := newSvc(t)
	launch.err = errors.New("boom")
	view, secret, err := svc.Create(Input{Name: "hook", Kind: KindWebhook})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	ts := unix(svc.now())
	res := svc.Receive(context.Background(), Hook{
		ID: view.ID, Body: body, Timestamp: ts, DeliveryID: "d1",
		Signature: SignWebhook(secret, ts, body), Event: "deploy",
	})
	svc.Wait()
	if res.Outcome != OutcomeError {
		t.Fatalf("result = %+v", res)
	}
}
