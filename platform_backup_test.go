package sdk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func backupTestClient(server *httptest.Server) *httpPlatformClient {
	return &httpPlatformClient{
		baseURL: server.URL,
		token:   "app-test-token",
		client:  server.Client(), slowClient: server.Client(), streamClient: server.Client(),
	}
}

func TestOpenPlatformSnapshotStreamsCallbackResponse(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/apps/callback/platform/snapshot" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer app-test-token" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = io.WriteString(w, "first-")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second")
	}))
	defer server.Close()

	client := backupTestClient(server)
	body, err := client.OpenPlatformSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	first := make([]byte, len("first-"))
	if _, err := io.ReadFull(body, first); err != nil || string(first) != "first-" {
		t.Fatalf("first chunk=%q err=%v", first, err)
	}
	close(release)
	rest, err := io.ReadAll(body)
	if err != nil || string(rest) != "second" {
		t.Fatalf("remaining=%q err=%v", rest, err)
	}
}

func TestRestorePlatformSnapshotSupportsKnownAndChunkedLengths(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int64
		wantLength  int64
		wantChunked bool
	}{{"known", 7, 7, false}, {"chunked", -1, -1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/apps/callback/platform/restore" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer app-test-token" || r.Header.Get("X-Confirm-Restore") != "yes" {
					t.Fatalf("headers auth=%q confirm=%q", r.Header.Get("Authorization"), r.Header.Get("X-Confirm-Restore"))
				}
				if r.ContentLength != tc.wantLength {
					t.Fatalf("content length=%d want=%d", r.ContentLength, tc.wantLength)
				}
				if tc.wantChunked && (len(r.TransferEncoding) != 1 || r.TransferEncoding[0] != "chunked") {
					t.Fatalf("transfer encoding=%v", r.TransferEncoding)
				}
				raw, _ := io.ReadAll(r.Body)
				if string(raw) != "archive" {
					t.Fatalf("body=%q", raw)
				}
				_, _ = io.WriteString(w, `{"restart_required":true}`)
			}))
			defer server.Close()
			client := backupTestClient(server)
			report, err := client.RestorePlatformSnapshot(context.Background(), bytes.NewBufferString("archive"), tc.size)
			if err != nil || report["restart_required"] != true {
				t.Fatalf("report=%v err=%v", report, err)
			}
		})
	}
}

func TestPlatformBackupCancellationPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := backupTestClient(server)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.OpenPlatformSnapshot(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestPlatformBackupErrorsAreUsefulAndBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "permission denied: "+strings.Repeat("x", platformBackupErrorLimit*2))
	}))
	defer server.Close()
	client := backupTestClient(server)
	_, err := client.OpenPlatformSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "http 403") {
		t.Fatalf("err=%v", err)
	}
	if len(err.Error()) > platformBackupErrorLimit+1024 {
		t.Fatalf("error was not bounded: %d bytes", len(err.Error()))
	}
}

func TestPlatformBackupPermissionsAreKnownAndDescribed(t *testing.T) {
	permissions := AllPermissions()
	for _, permission := range []Permission{PermPlatformBackupRead, PermPlatformBackupRestore} {
		if !containsPermission(permissions, permission) {
			t.Fatalf("missing permission %q", permission)
		}
		if PermissionDescription(permission) == string(permission) {
			t.Fatalf("permission %q lacks install-consent description", permission)
		}
	}
}

func containsPermission(values []Permission, target Permission) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
