package factory

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/providers/auth"
	"github.com/jonathanung/strike-cli/providers/chatgpt"
	"github.com/jonathanung/strike-cli/providers/openaicompat"
)

type memStore struct {
	creds map[string]auth.Credential
}

func (m *memStore) Get(provider string) (auth.Credential, bool) {
	c, ok := m.creds[provider]
	return c, ok
}

func (m *memStore) Set(provider string, c auth.Credential) error {
	if m.creds == nil {
		m.creds = map[string]auth.Credential{}
	}
	m.creds[provider] = c
	return nil
}

func TestSelectEcho(t *testing.T) {
	p, model, err := Select("echo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "echo" || model != "echo" {
		t.Fatalf("echo: name=%q model=%q", p.Name(), model)
	}
}

func TestSelectOpenAIAPIKeyVsOAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	st := &memStore{}

	t.Run("missing", func(t *testing.T) {
		_, _, err := Select("openai", Options{Store: st})
		if err == nil || !strings.Contains(err.Error(), "no OpenAI credentials") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("env key uses platform adapter", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "sk-env")
		p, _, err := Select("openai", Options{Store: st})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*openaicompat.Provider); !ok {
			t.Fatalf("env key type = %T, want *openaicompat.Provider", p)
		}
		if p.Name() != "openai" {
			t.Errorf("name = %q", p.Name())
		}
	})

	t.Run("stored api key uses platform adapter", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		st.creds = map[string]auth.Credential{
			"openai": {Type: auth.TypeAPIKey, APIKey: "sk-store"},
		}
		p, _, err := Select("openai", Options{Store: st})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*openaicompat.Provider); !ok {
			t.Fatalf("stored key type = %T", p)
		}
	})

	t.Run("oauth uses chatgpt adapter", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		// JWT with a ChatGPT account id so ChatGPTSource succeeds.
		idTok := fakeJWT(`{"chatgpt_account_id":"acct-1"}`)
		st.creds = map[string]auth.Credential{
			"openai": {
				Type:      auth.TypeOAuth,
				Access:    "oa-access",
				Refresh:   "r",
				IDToken:   idTok,
				ExpiresAt: time.Now().Add(time.Hour),
			},
		}
		p, _, err := Select("openai", Options{Store: st})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.(*chatgpt.Provider); !ok {
			t.Fatalf("oauth type = %T, want *chatgpt.Provider", p)
		}
		if p.Name() != "openai (chatgpt)" {
			t.Errorf("name = %q", p.Name())
		}
	})
}

func TestSelectGeminiAlias(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gk")
	t.Setenv("GOOGLE_API_KEY", "")
	p, _, err := Select("gemini", Options{Store: &memStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "google" {
		t.Errorf("name = %q", p.Name())
	}
}

func TestSelectDisabled(t *testing.T) {
	_, _, err := Select("anthropic", Options{
		Disabled: func(name string) bool { return name == "anthropic" },
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildCustomUnknownAPI(t *testing.T) {
	_, _, err := BuildCustom(Custom{Name: "bad", BaseURL: "https://x.example", API: "gemini"}, &memStore{})
	if err == nil || !strings.Contains(err.Error(), "unknown api") {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionalBearerEmpty(t *testing.T) {
	src := OptionalBearer("local", &memStore{}, "CUSTOM_KEY")
	tok, err := src(context.Background())
	if err != nil || tok != "" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
}

func fakeJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}
