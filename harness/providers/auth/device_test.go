package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestXAIDeviceFlowShape(t *testing.T) {
	d := XAIDeviceFlow()
	if d.ClientID != xaiClientID {
		t.Errorf("ClientID = %q", d.ClientID)
	}
	if !strings.Contains(d.DeviceURL, "/oauth2/device/code") {
		t.Errorf("DeviceURL = %q", d.DeviceURL)
	}
	if !strings.Contains(d.TokenURL, "/oauth2/token") {
		t.Errorf("TokenURL = %q", d.TokenURL)
	}
	if d.Scope == "" {
		t.Error("empty scope")
	}
}

func TestDeviceRequestCodeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		if vals.Get("client_id") != "cid" || vals.Get("scope") != "s" {
			t.Errorf("form = %v", vals)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-code",
			"user_code":                 "USER-1",
			"verification_uri":          "https://verify.test/",
			"verification_uri_complete": "https://verify.test/?code=USER-1",
			"expires_in":                600,
			"interval":                  5,
		})
	}))
	defer srv.Close()

	code, err := DeviceConfig{
		DeviceURL: srv.URL,
		TokenURL:  srv.URL + "/token",
		ClientID:  "cid",
		Scope:     "s",
	}.RequestCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if code.DeviceCode != "dev-code" || code.UserCode != "USER-1" {
		t.Fatalf("code = %+v", code)
	}
	if code.VerificationURIComplete == "" || code.ExpiresIn != 600 {
		t.Errorf("code = %+v", code)
	}
}

func TestDeviceRequestCodeErrors(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusBadRequest)
		}))
		defer srv.Close()
		_, err := DeviceConfig{DeviceURL: srv.URL, ClientID: "c"}.RequestCode(context.Background())
		if err == nil || !strings.Contains(err.Error(), "device code request failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing fields", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"device_code": "only"})
		}))
		defer srv.Close()
		_, err := DeviceConfig{DeviceURL: srv.URL, ClientID: "c"}.RequestCode(context.Background())
		if err == nil || !strings.Contains(err.Error(), "missing required fields") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not-json"))
		}))
		defer srv.Close()
		_, err := DeviceConfig{DeviceURL: srv.URL, ClientID: "c"}.RequestCode(context.Background())
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDevicePollSuccess(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		if vals.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Errorf("grant_type = %q", vals.Get("grant_type"))
		}
		if vals.Get("device_code") != "dc" || vals.Get("client_id") != "cid" {
			t.Errorf("form = %v", vals)
		}
		i := n.Add(1)
		if i == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "authorization_pending",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-dev",
			"refresh_token": "refresh-dev",
			"expires_in":    120,
		})
	}))
	defer srv.Close()

	// Interval is floored at 5s in Poll; first wait then pending, second wait then tokens.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tok, err := DeviceConfig{TokenURL: srv.URL, ClientID: "cid"}.Poll(ctx, &DeviceCode{
		DeviceCode: "dc",
		Interval:   1, // floored to 5
		ExpiresIn:  60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "access-dev" || tok.Refresh != "refresh-dev" {
		t.Fatalf("tokens = %+v", tok)
	}
	if n.Load() < 2 {
		t.Errorf("polls = %d, want >= 2", n.Load())
	}
}

func TestDevicePollDeniedAndExpired(t *testing.T) {
	cases := []struct {
		name    string
		errCode string
		want    string
	}{
		{"denied", "access_denied", "denied"},
		{"auth denied", "authorization_denied", "denied"},
		{"expired", "expired_token", "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.errCode})
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := DeviceConfig{TokenURL: srv.URL, ClientID: "c"}.Poll(ctx, &DeviceCode{
				DeviceCode: "dc",
				Interval:   5,
				ExpiresIn:  30,
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDevicePollSlowDownThenSuccess(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "after-slow",
			"expires_in":   60,
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := DeviceConfig{TokenURL: srv.URL, ClientID: "c"}.Poll(ctx, &DeviceCode{
		DeviceCode: "dc",
		Interval:   5,
		ExpiresIn:  60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "after-slow" {
		t.Fatalf("tokens = %+v", tok)
	}
}

func TestDevicePollContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := DeviceConfig{TokenURL: srv.URL, ClientID: "c"}.Poll(ctx, &DeviceCode{
		DeviceCode: "dc",
		Interval:   5,
		ExpiresIn:  300,
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestDevicePollTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	}))
	defer srv.Close()

	// expires_in defaults to 300 when <=0; use 1 so the first 5s wait overshoots the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	_, err := DeviceConfig{TokenURL: srv.URL, ClientID: "c"}.Poll(ctx, &DeviceCode{
		DeviceCode: "dc",
		Interval:   5,
		ExpiresIn:  1,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timed out", err)
	}
	if time.Since(start) > 12*time.Second {
		t.Errorf("took %v, expected ~5s floor wait then timeout", time.Since(start))
	}
}
