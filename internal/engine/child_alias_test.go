package engine

import (
	"context"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

type aliasTestProvider struct{}

func (aliasTestProvider) Name() string { return "google" }

func (aliasTestProvider) Stream(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
	return nil, nil
}

func TestResolveTaskModelPinCanonicalizesGeminiAlias(t *testing.T) {
	e := &Engine{prov: aliasTestProvider{}, provName: "google"}
	pin, err := e.resolveTaskModelPin(context.Background(), "gemini/gemini-2.5-flash")
	if err != nil {
		t.Fatalf("resolveTaskModelPin: %v", err)
	}
	if pin.provider != "google" || pin.model != "gemini-2.5-flash" {
		t.Fatalf("pin = %+v, want google/gemini-2.5-flash", pin)
	}
	if pin.prov != nil {
		t.Fatal("same-provider alias unexpectedly selected a second provider")
	}
}
