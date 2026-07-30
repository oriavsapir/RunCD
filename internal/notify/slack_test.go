package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlackSink_SendsExpectedPayload(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &SlackSink{WebhookURL: srv.URL}
	if err := sink.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody["text"] != "hello" {
		t.Fatalf("expected text=hello, got %+v", gotBody)
	}
}

func TestSlackSink_NonSuccessStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := &SlackSink{WebhookURL: srv.URL}
	if err := sink.Send(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}
