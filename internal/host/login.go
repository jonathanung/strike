package host

import (
	"context"
	"errors"
)

// OAuthLogin is an in-flight browser login. Wait blocks until the callback
// or ctx cancel and returns a user-facing outcome message. Credentials are
// persisted internally; frontends never see tokens.
type OAuthLogin struct {
	URL  string
	wait func(context.Context) (string, error)
}

// NewOAuthLogin builds an OAuthLogin from the authorize URL and a wait
// closure. The wait closure may be nil, in which case Wait returns an error.
func NewOAuthLogin(url string, wait func(context.Context) (string, error)) *OAuthLogin {
	return &OAuthLogin{URL: url, wait: wait}
}

// Wait blocks until the browser callback arrives or ctx is canceled, then
// returns a user-facing outcome message. A nil wait closure is an error.
func (l *OAuthLogin) Wait(ctx context.Context) (string, error) {
	if l.wait == nil {
		return "", errors.New("host: OAuthLogin has no wait function")
	}
	return l.wait(ctx)
}

// DeviceLogin is an in-flight RFC 8628 device authorization.
type DeviceLogin struct {
	UserCode        string
	VerificationURI string
	poll            func(context.Context) (string, error)
}

// NewDeviceLogin builds a DeviceLogin from the user code, verification URI,
// and a poll closure. The poll closure may be nil, in which case Poll
// returns an error.
func NewDeviceLogin(userCode, verificationURI string, poll func(context.Context) (string, error)) *DeviceLogin {
	return &DeviceLogin{UserCode: userCode, VerificationURI: verificationURI, poll: poll}
}

// Poll blocks until the device authorization completes or ctx is canceled,
// then returns a user-facing outcome message. A nil poll closure is an error.
func (l *DeviceLogin) Poll(ctx context.Context) (string, error) {
	if l.poll == nil {
		return "", errors.New("host: DeviceLogin has no poll function")
	}
	return l.poll(ctx)
}
