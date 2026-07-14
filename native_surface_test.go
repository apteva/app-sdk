package sdk

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNativeSurfaceJSONSchemaIsValidJSON(t *testing.T) {
	data, err := os.ReadFile("schemas/apteva-native-surface-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatal("native surface JSON Schema is not valid JSON")
	}
}

const validNativeSurfaceJSON = `{
  "schema": "apteva-native-surface/v1",
  "id": "files",
  "version": "1.0.0",
  "title": "Files",
  "description": "Browse and manage project files.",
  "icon": "folder",
  "context": {"scope": "project"},
  "state": {
    "folder": {"type": "string", "default": "/", "persistence": "project"},
    "query": {"type": "string", "default": ""},
    "recursive": {"type": "boolean", "default": false}
  },
  "data_sources": {
    "files": {
      "request": {"method": "GET", "path": "/files", "query": {"folder": "$state.folder", "q": "$state.query", "recursive": "$state.recursive"}},
      "response": {"items": "$.files", "id": "$.id"},
      "pagination": {"type": "page", "request_key": "offset", "page_size": 50},
      "refresh": {"on_appear": true, "pull_to_refresh": true}
    },
    "file": {
      "request": {"method": "GET", "path": "/files/{item.id}"},
      "response": {"item": "$", "id": "$.id"}
    },
    "folders": {
      "request": {"method": "GET", "path": "/folders", "query": {"format": "picker"}},
      "response": {"items": "$.items", "id": "$.id"}
    }
  },
  "primary_actions": ["upload"],
  "sections": [{
    "id": "files",
    "kind": "collection",
    "title": "Files",
    "source": "files",
    "presentation": "adaptive",
    "search": {"state": "query", "placeholder": "Search files", "query_key": "q"},
    "filters": [{"id": "recursive", "type": "toggle", "state": "recursive", "label": "Include subfolders", "query_key": "recursive"}],
    "item": {
      "id": "$item.id",
      "title": "$item.name",
      "subtitle": [{"value": "$item.folder"}, {"value": "$item.size_bytes", "format": "file_size"}],
      "icon": {"value": "$item.content_type", "format": "file_icon"},
      "badge": {"value": "$item.visibility"},
      "destination": "file_detail",
      "actions": ["share", "delete"]
    },
    "empty": {"title": "No files", "description": "Upload a file to get started.", "icon": "folder"},
    "loading": {"description": "Loading files"},
    "error": {"title": "Could not load files", "retry": true}
  }],
  "destinations": {
    "file_detail": {
      "type": "detail",
      "title": "{item.name}",
      "source": "file",
      "sections": [
        {"id": "preview", "kind": "file_preview", "preview": {"path": "/files/{item.id}/content/{item.name}", "name": "$item.name", "content_type": "$item.content_type", "share_action": "share", "delete_action": "delete"}},
        {"id": "properties", "kind": "properties", "fields": [{"label": "Size", "value": "$item.size_bytes", "format": "file_size"}]}
      ],
      "actions": ["rename", "share", "delete"]
    }
  },
  "actions": {
    "upload": {
      "type": "file_upload", "label": "Upload", "icon": "upload", "placements": ["primary"],
      "request": {"method": "POST", "path": "/files", "encoding": "multipart", "body": {"file": "$input.file", "folder": "$state.folder"}},
      "fields": [{"name": "file", "type": "file", "required": true}, {"name": "folder", "type": "resource", "resource": "folder"}],
      "success": {"refresh": ["files"], "dismiss": true, "toast": "Uploaded"}
    },
    "rename": {
      "type": "form", "label": "Rename", "request": {"method": "PATCH", "path": "/files/{item.id}", "encoding": "json", "body": {"name": "$input.name"}},
      "fields": [{"name": "name", "type": "string", "required": true}], "success": {"refresh": ["files"], "dismiss": true}
    },
    "share": {
      "type": "share_result", "label": "Share", "request": {"method": "POST", "path": "/files/{item.id}/url", "encoding": "json"}, "result": "$.url"
    },
    "delete": {
      "type": "confirm", "label": "Delete", "destructive": true, "request": {"method": "DELETE", "path": "/files/{item.id}"},
      "confirmation": {"title": "Delete this file?", "message": "This cannot be undone.", "confirm_label": "Delete"},
      "success": {"refresh": ["files"], "dismiss": true}
    }
  },
  "resources": {
    "folder": {"type": "tree", "label": "Folder", "source": "folders", "mapping": {"id": "$.id", "label": "$.label", "parent": "$.parent"}}
  }
}`

func TestParseNativeSurfaceComplete(t *testing.T) {
	surface, err := ParseNativeSurface([]byte(validNativeSurfaceJSON))
	if err != nil {
		t.Fatal(err)
	}
	if surface.ID != "files" || len(surface.Actions) != 4 || len(surface.Destinations) != 1 {
		t.Fatalf("unexpected surface: %#v", surface)
	}
	descriptor := UISurface{ID: "files", Schema: NativeSurfaceSchemaCurrent}
	if err := ValidateNativeSurfaceForDescriptor(surface, descriptor); err != nil {
		t.Fatal(err)
	}
}

func TestParseNativeSurfaceRejectsUnsafeOrInvalidDocuments(t *testing.T) {
	tests := []struct{ name, from, to, want string }{
		{"unknown field", `"icon": "folder",`, `"icon": "folder", "script": "alert(1)",`, "unknown field"},
		{"trailing json", "", "", "trailing value"},
		{"absolute path", `"path": "/files"`, `"path": "https://evil.example/files"`, "app-relative"},
		{"encoded traversal", `"path": "/files"`, `"path": "/%2e%2e/secrets"`, "app-relative"},
		{"bad method", `"method": "GET"`, `"method": "PUT"`, "unsupported"},
		{"expression", `"folder": "$state.folder"`, `"folder": "{system.exec()}"`, "unsupported template expression"},
		{"project injection", `"folder": "$state.folder"`, `"project_id": "stolen", "folder": "$state.folder"`, "must not declare project_id"},
		{"unknown state", `"state": "query"`, `"state": "missing"`, "unknown state"},
		{"destructive no confirm", `"confirmation": {"title": "Delete this file?", "message": "This cannot be undone.", "confirm_label": "Delete"},`, ``, "requires confirmation"},
		{"upload no required file", `{"name": "file", "type": "file", "required": true}`, `{"name": "file", "type": "file"}`, "exactly one required file"},
		{"default mismatch", `"recursive": {"type": "boolean", "default": false}`, `"recursive": {"type": "boolean", "default": "no"}`, "does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := validNativeSurfaceJSON
			if test.name == "trailing json" {
				raw += ` {}`
			} else {
				raw = strings.Replace(raw, test.from, test.to, 1)
			}
			_, err := ParseNativeSurface([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseNativeSurfaceRejectsOversize(t *testing.T) {
	_, err := ParseNativeSurface(make([]byte, NativeSurfaceMaxBytes+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateNativeSurfaceForDescriptorRejectsMismatch(t *testing.T) {
	surface, err := ParseNativeSurface([]byte(validNativeSurfaceJSON))
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateNativeSurfaceForDescriptor(surface, UISurface{ID: "contacts", Schema: NativeSurfaceSchemaCurrent})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}
