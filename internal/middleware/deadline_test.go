package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestExtendWriteDeadline runs a real server with a short WriteTimeout: a
// slow handler's response is lost without the extension and delivered with
// it, also through a non-wrapping middleware chain.
func TestExtendWriteDeadline(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	})
	get := func(h http.Handler) (string, error) {
		srv := httptest.NewUnstartedServer(h)
		srv.Config.WriteTimeout = 100 * time.Millisecond
		srv.Start()
		defer srv.Close()
		resp, err := http.Get(srv.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}

	if body, err := get(slow); err == nil && body == "done" {
		t.Fatal("control: the server timeout should have cut the slow response")
	}
	if body, err := get(ExtendWriteDeadline(2 * time.Second)(slow)); err != nil || body != "done" {
		t.Errorf("extended: body %q err %v, want the full response", body, err)
	}
}

// TestExtendWriteDeadlineUnsupported: a ResponseWriter without a connection
// (no deadline support) still runs the handler.
func TestExtendWriteDeadlineUnsupported(t *testing.T) {
	ran := false
	h := ExtendWriteDeadline(time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !ran {
		t.Error("handler did not run")
	}
}
