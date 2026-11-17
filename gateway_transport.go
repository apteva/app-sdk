package sdk

import (
	"net/http"
	"sync"
)

// gatewayTransport is shared by every client that talks to the platform
// gateway. http.DefaultTransport keeps only two idle connections per host, so
// concurrent CallApp/CallAppResult traffic closed most connections after one
// use and could exhaust local ports under load. No MaxConnsPerHost: stream
// requests would hold the slots.
var gatewayTransport = sync.OnceValue(func() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	tr := base.Clone()
	tr.MaxIdleConns = 256
	tr.MaxIdleConnsPerHost = 64
	return tr
})
