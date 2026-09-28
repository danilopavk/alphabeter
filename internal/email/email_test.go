package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var msg = Message{
	From:    "Alphabeter <no-reply@alphabeter.example>",
	To:      "parent@example.com",
	Subject: "Verify your email",
	Text:    "Open https://alphabeter.example/oauth/verify?token=abc",
	HTML:    "<p>Open <a href=\"https://alphabeter.example/oauth/verify?token=abc\">this link</a></p>",
}

func TestResendSendsTheMessage(t *testing.T) {
	var got struct {
		method, path, auth, contentType string
		body                            map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.auth, got.contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Write([]byte(`{"id":"49a3999c"}`))
	}))
	defer srv.Close()

	r := &Resend{APIKey: "re_test", BaseURL: srv.URL}
	if err := r.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/emails" || got.auth != "Bearer re_test" || got.contentType != "application/json" {
		t.Errorf("request %s %s auth=%q type=%q", got.method, got.path, got.auth, got.contentType)
	}
	want := map[string]any{
		"from": msg.From, "to": []any{msg.To}, "subject": msg.Subject, "text": msg.Text, "html": msg.HTML,
	}
	for k, v := range want {
		gotJSON, _ := json.Marshal(got.body[k])
		wantJSON, _ := json.Marshal(v)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("body[%s] = %s, want %s", k, gotJSON, wantJSON)
		}
	}
}

func TestResendReportsRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"statusCode":403,"name":"validation_error","message":"The alphabeter.example domain is not verified."}`)
	}))
	defer srv.Close()

	err := (&Resend{APIKey: "re_test", BaseURL: srv.URL}).Send(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "domain is not verified") {
		t.Errorf("err = %v", err)
	}
}

func TestResendDefaultsToTheRealAPI(t *testing.T) {
	if got := (&Resend{}).baseURL(); got != "https://api.resend.com" {
		t.Errorf("base URL %q", got)
	}
}

func TestRecorder(t *testing.T) {
	var r Recorder
	r.Send(context.Background(), msg)
	other := msg
	other.To = "other@example.com"
	r.Send(context.Background(), other)

	last, ok := r.Last("parent@example.com")
	if !ok || last != msg {
		t.Errorf("Last = %+v, %v", last, ok)
	}
	if _, ok := r.Last("nobody@example.com"); ok {
		t.Error("found a message for an address that got none")
	}
}
