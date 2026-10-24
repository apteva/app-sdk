package sdk

// RealtimeMediaConfig requests an optional media path for a new realtime session.
// Transport selects the Core/provider connection (websocket or webrtc); Format
// selects bridge audio (pcm16, g711_ulaw or g711_alaw). Support is negotiated by
// Server/Core and the selected provider; these fields do not change SIP routing.
type RealtimeMediaConfig struct {
	Transport string `json:"transport,omitempty"`
	Format    string `json:"format,omitempty"`
}
