package sdk

import (
	"encoding/base64"
	"fmt"
)

// MCPResult opts a tool into native MCP content. Ordinary tool return values
// retain their JSON-as-text representation. Use image blocks to supply pixels
// to the calling model, rather than serializing base64 into its text context.
// The app-to-app JSON transport returns this object unchanged.
type MCPResult struct {
	Content           []MCPContent   `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// MCPContent is a text or base64 image block in a native tool result.
type MCPContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	// Meta is the block's MCP _meta: extra information for the client that
	// is not shown to the model as content. Clients ignore keys they do not
	// know. Apteva Core reads io.apteva/label (the image's caption) and
	// io.apteva/retention ("keep", or "latest" when a newer image from the
	// same tool replaces it) on image blocks.
	Meta map[string]any `json:"_meta,omitempty"`
}

func (r MCPResult) MarshalJSON() ([]byte, error) {
	type wire MCPResult
	if r.Content == nil {
		r.Content = []MCPContent{}
	}
	return marshalJSONNoHTMLEscape(wire(r))
}

// MarshalJSON keeps the required text field even for empty text and prevents
// malformed image blocks from reaching clients as apparently successful reads.
func (c MCPContent) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case "text":
		return marshalJSONNoHTMLEscape(struct {
			Type string         `json:"type"`
			Text string         `json:"text"`
			Meta map[string]any `json:"_meta,omitempty"`
		}{c.Type, c.Text, c.Meta})
	case "image":
		if c.MIMEType == "" || c.Data == "" {
			return nil, fmt.Errorf("MCP image requires mimeType and base64 data")
		}
		if _, err := base64.StdEncoding.DecodeString(c.Data); err != nil {
			return nil, fmt.Errorf("invalid MCP image base64: %w", err)
		}
		type wire MCPContent
		return marshalJSONNoHTMLEscape(wire(c))
	default:
		return nil, fmt.Errorf("unsupported MCP content type %q", c.Type)
	}
}
