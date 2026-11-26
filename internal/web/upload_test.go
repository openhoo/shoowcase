package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestSignedUploadPreflightAndCompletion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "upload.sqlite")
	_, db := handlerFixture(t, dbPath, "team-a", true)
	user := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	rev := seedSiteFor(t, db, user)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A previously public site now lives on a globally private instance.
	a, _ := handlerFixture(t, dbPath, "team-a", false)
	ctl, _ := browserSessions(t, a, testSite)
	data := []byte("p{color:blue}")
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	prepare := func(path string, size int, id string) (map[string]any, error) {
		b, err := json.Marshal(uploadSpec{Site: "field", Revision: rev, Path: path, SHA: sum, Size: int64(size), Request: id})
		if err != nil {
			t.Fatal(err)
		}
		return a.prepare(t.Context(), user, b)
	}
	for _, tc := range []struct {
		path string
		size int
	}{
		{"auth/logo.png", 100}, {"docs/.hidden/file.png", 20}, {"logo.svg", 100},
		{"sheet.css", sites.MaxTextBytes + 1}, {"avatar.png", sites.MaxFileBytes + 1},
	} {
		out, err := prepare(tc.path, tc.size, "bad-target")
		if errorCode(err) != "invalid_argument" || out != nil {
			t.Fatalf("%s should fail before issuing an upload URL: %v", tc.path, err)
		}
	}
	for _, kind := range []string{"put", "form-json", "form-html"} {
		// Consecutive dots inside a filename are valid in the inline uploader too.
		pre, err := prepare("styles/"+kind+"..min.CSS", len(data), "upload-"+kind)
		if err != nil {
			t.Fatal(err)
		}
		link := pre["upload_url"].(string)
		var req *http.Request
		if kind == "put" {
			req = httptest.NewRequest("PUT", link, bytes.NewReader(data))
			req.Header.Set("Accept", "text/html")
		} else {
			page := hitRoute(t, a, "GET", link, ctl, nil)
			match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
			if page.Code != 200 || len(match) != 2 || !strings.Contains(page.Body.String(), sum) || !strings.Contains(page.Body.String(), fmt.Sprintf("%d bytes", len(data))) {
				t.Fatal("browser upload form missing the expected file metadata or CSRF")
			}
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			if err := form.WriteField("csrf", match[1]); err != nil {
				t.Fatal(err)
			}
			upload, err := form.CreateFormFile("file", "screen.css")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := upload.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := form.Close(); err != nil {
				t.Fatal(err)
			}
			req = httptest.NewRequest("POST", link, &body)
			req.Header.Set("Origin", testControl)
			req.Header.Set("Content-Type", form.FormDataContentType())
			req.AddCookie(ctl)
			if kind == "form-json" {
				req.Header.Set("Accept", "application/json")
			} else {
				req.Header.Set("Accept", "text/html,application/xhtml+xml")
			}
		}
		req.RemoteAddr = "127.0.0.1:45678"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s completion HTTP %d: %s", kind, w.Code, w.Body.String())
		}
		if kind == "form-html" {
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "Upload complete") {
				t.Fatalf("expected readable upload confirmation: %s", w.Body.String())
			}
			continue
		}
		var result map[string]any
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("%s should return JSON", kind)
		}
		newRev, _ := result["revision"].(string)
		preview, _ := result["preview_url"].(string)
		if newRev == "" || newRev == rev || preview == "" || result["visibility"] != "workspace" || result["configured_visibility"] != "public" {
			t.Fatalf("%s did not report the draft and effective workspace access", kind)
		}
		rev = newRev
	}
}
