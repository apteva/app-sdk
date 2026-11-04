package sdk

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ThreadProviderSelectionVersion is what PlatformInfo advertises on servers
// that accept ThreadSpawnRequest.Provider.
const ThreadProviderSelectionVersion = 1

var (
	// ErrThreadProviderUnsupported means the platform is too old to apply
	// ThreadSpawnRequest.Provider. Nothing was created.
	ErrThreadProviderUnsupported = errors.New("platform does not support choosing a thread's provider")
	// ErrThreadProviderUnavailable means the target agent can't use the
	// requested provider. No thread was created.
	ErrThreadProviderUnavailable = errors.New("provider is not available to this agent")
	// ErrThreadProviderConflict means the thread already exists on a different
	// provider. Running threads are never moved, so the thread is unchanged. Its
	// events weren't delivered either, unless this request lost a race to create
	// the thread: Core had then already queued them. Kill the thread and spawn it
	// again to change its provider.
	ErrThreadProviderConflict = errors.New("thread already uses a different provider")
)

// ThreadProviderStatus reports a thread's provider as the agent has it set up.
//
// A pin is a preference. If its provider is removed from the agent, the thread
// keeps working on its parent's provider with Missing set, and returns to the
// pin once the provider is back. Repeating the same pin while it's missing still
// succeeds, so delivering events to an existing thread doesn't stop.
//
// Active says which provider the thread will try first, not where any one call
// ran: if a request fails, Core may retry that request on another provider in the
// agent's pool. Tiers within the provider are still Core's to choose.
type ThreadProviderStatus struct {
	// Pin is the provider the thread was asked to use. Empty means it follows
	// its parent.
	Pin string `json:"pin,omitempty"`
	// Active is the provider the thread is currently set to use.
	Active string `json:"active,omitempty"`
	// Missing means Pin isn't configured on the agent right now, so Active is
	// something else.
	Missing bool `json:"missing,omitempty"`
}

// requireThreadProviderSelection fails before anything is created on a server
// that would drop the provider and quietly run the thread on its parent's.
// A failed check is returned as it is, because it says nothing about the server.
func (c *httpPlatformClient) requireThreadProviderSelection() error {
	c.piMu.Lock()
	supported := c.piCached != nil && time.Since(c.piCachedAt) < piCacheTTL &&
		c.piCached.ThreadProviderSelection >= ThreadProviderSelectionVersion
	c.piMu.Unlock()
	if supported {
		return nil
	}
	var fresh PlatformInfo
	if err := c.get("/api/apps/callback/platform-info", &fresh); err != nil {
		var httpErr *platformHTTPError
		if errors.As(err, &httpErr) && httpErr.status == http.StatusNotFound {
			return ErrThreadProviderUnsupported
		}
		return fmt.Errorf("check thread provider support: %w", err)
	}
	c.piMu.Lock()
	c.piCached = &fresh
	c.piCachedAt = time.Now()
	c.piMu.Unlock()
	if fresh.ThreadProviderSelection < ThreadProviderSelectionVersion {
		return ErrThreadProviderUnsupported
	}
	return nil
}

// threadProviderRequestError gives the refusals an app can act on their own
// errors. Requests without a provider keep the error they always had.
func threadProviderRequestError(provider string, err error) error {
	var httpErr *platformHTTPError
	if provider == "" || !errors.As(err, &httpErr) {
		return err
	}
	switch httpErr.status {
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrThreadProviderConflict, strings.TrimSpace(httpErr.body))
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrThreadProviderUnavailable, strings.TrimSpace(httpErr.body))
	}
	return err
}
