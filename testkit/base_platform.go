package testkit

import (
	"encoding/json"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// ErrNotImplemented is returned by every BasePlatformClient method
// that the embedding test stub hasn't overridden. Tests that exercise
// a code path the stub didn't bother to wire should fail loudly with
// this error rather than silently returning zero values.
var ErrNotImplemented = errors.New("testkit: method not implemented on BasePlatformClient — embed and override the methods your test exercises")

// BasePlatformClient is a default implementation of sdk.PlatformClient
// every method of which returns ErrNotImplemented (or a zero value
// where the signature doesn't return an error). Test stubs embed
// this struct so they only override the methods the test actually
// touches — and adding a method to PlatformClient doesn't ripple
// through every consumer's stubs.
//
// Usage:
//
//	type myStub struct {
//	    testkit.BasePlatformClient
//	    callApp func(name, tool string, in map[string]any) (json.RawMessage, error)
//	}
//
//	func (s *myStub) CallApp(name, tool string, in map[string]any) (json.RawMessage, error) {
//	    return s.callApp(name, tool, in)
//	}
//
// All other PlatformClient methods inherit the no-op default. When a
// test path unexpectedly hits one of those defaults, you get
// ErrNotImplemented with a stack trace pointing at the call site
// instead of a confusing nil/empty surface.
type BasePlatformClient struct{}

// Static interface assertion — fail compilation if BasePlatformClient
// drifts from sdk.PlatformClient.
var _ sdk.PlatformClient = (*BasePlatformClient)(nil)

func (BasePlatformClient) GetConnection(int64) (*sdk.PlatformConnection, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) GetInstance(int64) (*sdk.PlatformInstance, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) SendEvent(int64, string) error {
	return ErrNotImplemented
}

func (BasePlatformClient) SendToChannel(string, string, string) error {
	return ErrNotImplemented
}

func (BasePlatformClient) WhoAmI() (*sdk.InstallIdentity, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) ExecuteIntegrationTool(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) CallAppResult(string, string, map[string]any, any) error {
	return ErrNotImplemented
}

func (BasePlatformClient) StartOAuth(sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) DisconnectConnection(int64) error {
	return ErrNotImplemented
}

func (BasePlatformClient) ListOwnedConnections() ([]sdk.PlatformConnection, error) {
	return nil, ErrNotImplemented
}

func (BasePlatformClient) GetGrants(int64) (*sdk.GrantsResponse, error) {
	// Return default-allow so apps gated on grants don't 403 in tests
	// that haven't wired a stub. This matches httpPlatformClient's
	// fallback behavior against pre-grants servers.
	return &sdk.GrantsResponse{DefaultEffect: "allow"}, nil
}

func (BasePlatformClient) GetConnectionCredentials(int64) (*sdk.ConnectionCredentials, error) {
	return nil, ErrNotImplemented
}
