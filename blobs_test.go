package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlobClientUploadAndCommonHandle(t *testing.T) {
	seen := make(chan StoreBlobRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/blobs" || r.Header.Get("Authorization") != "Bearer app-token" {
			t.Errorf("unexpected upload transport: %s", r.URL.Path)
		}
		var upload StoreBlobRequest
		if err := json.NewDecoder(r.Body).Decode(&upload); err != nil {
			t.Error(err)
		}
		seen <- upload
		_ = json.NewEncoder(w).Encode(FileHandle{File: true, Ref: "blobref://opaque", Filename: upload.Filename, MIMEType: upload.MIMEType, Size: int64(len(upload.Data))})
	}))
	defer server.Close()
	client := &httpPlatformClient{baseURL: server.URL, token: "app-token", client: server.Client(), slowClient: server.Client()}
	scoped := &projectScopedClient{inner: client, projectID: "project"}
	request := StoreBlobRequest{Scope: FileReferenceScope{AgentID: 1, ThreadID: "thread"}, Filename: "input.txt", MIMEType: "text/plain", Data: []byte("input bytes")}
	handle, err := scoped.StoreBlob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	upload := <-seen
	if upload.Scope.ProjectID != "project" || string(upload.Data) != "input bytes" {
		t.Fatalf("upload changed: %#v", upload)
	}
	part, _ := json.Marshal(handle.ContentPart())
	if !strings.Contains(string(part), `"type":"file_ref"`) || !strings.Contains(string(part), `"ref":"blobref://opaque"`) || strings.Contains(string(part), "input bytes") {
		t.Fatalf("incoming handle: %s", part)
	}
	request.Scope.ProjectID = "other-project"
	if _, err := scoped.StoreBlob(context.Background(), request); err == nil {
		t.Fatal("scoped client allowed another project")
	}
	for _, ref := range []string{"blobref://x", "apteva-file://legacy"} {
		if !IsFileReference(ref) {
			t.Fatalf("reference not recognized: %s", ref)
		}
	}
}
