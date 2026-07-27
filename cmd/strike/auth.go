package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jonathanung/strike-cli/internal/auth"
)

const authUsage = `Manage provider credentials.

Usage:
  strike auth login <anthropic|openai|xai|gemini|kimi|deepseek> [--api-key] [--device]
  strike auth status
  strike auth logout <provider>

Login methods:
  openai      OAuth "Sign in with ChatGPT" (default), or --api-key to paste a key
  xai         OAuth browser flow (default), --device for headless machines,
              or --api-key to paste a key
  anthropic   --api-key (paste a key; OAuth not wired yet)
  gemini      --api-key (paste a key; OAuth not wired yet)
  kimi        --api-key (paste a key; OAuth not supported)
  deepseek    --api-key (paste a key; OAuth not supported)

API keys can also be provided via ANTHROPIC_API_KEY / OPENAI_API_KEY /
XAI_API_KEY / GEMINI_API_KEY / KIMI_API_KEY / DEEPSEEK_API_KEY, which take
precedence over stored credentials.`

func runAuth(args []string, output io.Writer) error {
	store, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		return fmt.Errorf("opening auth store: %w", err)
	}
	if len(args) == 0 {
		fmt.Fprintln(output, authUsage)
		return nil
	}
	switch args[0] {
	case "login":
		return runAuthLogin(store, args[1:], output)
	case "status":
		return runAuthStatus(store, output)
	case "logout":
		if len(args) < 2 {
			return fmt.Errorf("usage: strike auth logout <provider>")
		}
		if err := store.Delete(args[1]); err != nil {
			return err
		}
		fmt.Fprintln(output, "Logged out of", args[1])
		return nil
	default:
		fmt.Fprintln(output, authUsage)
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func runAuthLogin(store *auth.Store, args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: strike auth login <anthropic|openai|xai> [--api-key] [--device]")
	}
	prov := args[0]
	useAPIKey := hasFlag(args[1:], "--api-key")
	useDevice := hasFlag(args[1:], "--device")
	ctx := context.Background()

	switch prov {
	case "anthropic":
		return loginAPIKey(store, prov, output)
	case "openai":
		if useAPIKey {
			return loginAPIKey(store, prov, output)
		}
		return loginOpenAIOAuth(ctx, store, output)
	case "xai":
		if useAPIKey {
			return loginAPIKey(store, prov, output)
		}
		return loginXAIOAuth(ctx, store, useDevice, output)
	case "gemini":
		return loginAPIKey(store, prov, output)
	case "kimi", "deepseek":
		return loginAPIKey(store, prov, output)
	default:
		return fmt.Errorf("unknown provider %q (want anthropic, openai, xai, gemini, kimi, or deepseek)", prov)
	}
}

func loginAPIKey(store *auth.Store, prov string, output io.Writer) error {
	key, err := promptSecret(output, fmt.Sprintf("Paste your %s API key: ", prov))
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("no key entered")
	}
	if err := store.Set(prov, auth.Credential{Type: auth.TypeAPIKey, APIKey: key}); err != nil {
		return err
	}
	fmt.Fprintf(output, "Stored %s API key in %s\n", prov, auth.DefaultPath())
	return nil
}

func loginOpenAIOAuth(ctx context.Context, store *auth.Store, output io.Writer) error {
	tokens, err := auth.OpenAIFlow().Login(ctx)
	if err != nil {
		return err
	}
	outcome, err := auth.CompleteLogin(ctx, store, "openai", tokens)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, outcome)
	return nil
}

func loginXAIOAuth(ctx context.Context, store *auth.Store, device bool, output io.Writer) error {
	var tokens *auth.Tokens
	var err error
	if device {
		flow := auth.XAIDeviceFlow()
		code, reqErr := flow.RequestCode(ctx)
		if reqErr != nil {
			return reqErr
		}
		fmt.Fprintf(output, "Open %s on any device and enter code: %s\n", code.VerificationURI, code.UserCode)
		if code.VerificationURIComplete != "" {
			fmt.Fprintln(output, "Or open directly:", code.VerificationURIComplete)
		}
		fmt.Fprintln(output, "Waiting for authorization…")
		tokens, err = flow.Poll(ctx, code)
	} else {
		tokens, err = auth.XAIFlow().Login(ctx)
	}
	if err != nil {
		return err
	}
	outcome, err := auth.CompleteLogin(ctx, store, "xai", tokens)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, outcome)
	return nil
}

// builtinAuthProviders are the credential-backed providers `strike auth`
// knows by name, in display order.
var builtinAuthProviders = []string{"anthropic", "openai", "xai", "gemini", "kimi", "deepseek"}

func runAuthStatus(store *auth.Store, output io.Writer) error {
	providers := store.Providers()
	for _, name := range builtinAuthProviders {
		status := "not logged in"
		if key, ok := envKey(name); ok {
			status = "using " + key + " from environment"
		} else if cred, ok := store.Get(name); ok {
			switch {
			case cred.Type == auth.TypeAPIKey:
				status = "API key stored"
			case cred.APIKey != "":
				status = "OAuth + exchanged API key"
			case !cred.ExpiresAt.IsZero() && time.Now().After(cred.ExpiresAt):
				status = "OAuth (access token expired; will refresh on use)"
			default:
				status = "OAuth"
			}
		}
		fmt.Fprintf(output, "  %-10s %s\n", name, status)
	}
	for _, p := range providers {
		if !slices.Contains(builtinAuthProviders, p) {
			fmt.Fprintf(output, "  %-10s credential stored (unknown provider)\n", p)
		}
	}
	return nil
}

func envKey(provider string) (string, bool) {
	name := map[string]string{
		"anthropic": "ANTHROPIC_API_KEY",
		"openai":    "OPENAI_API_KEY",
		"xai":       "XAI_API_KEY",
		"gemini":    "GEMINI_API_KEY",
		"kimi":      "KIMI_API_KEY",
		"deepseek":  "DEEPSEEK_API_KEY",
	}[provider]
	if name != "" && os.Getenv(name) != "" {
		return name, true
	}
	return "", false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func promptSecret(output io.Writer, prompt string) (string, error) {
	fmt.Fprint(output, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		key, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(output)
		return strings.TrimSpace(string(key)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
