package larkagent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	serviceim "github.com/liuchong/lark-agent/internal/lark"
)

func TestRealtimeSDKDoesNotLogWebSocketCredentials(t *testing.T) {
	const (
		accessKeyCanary = "integration-access-key-canary"
		ticketCanary    = "integration-ticket-canary"
	)

	connected := make(chan *websocket.Conn, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/callback/ws/endpoint":
			websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") +
				"/ws?device_id=1&service_id=1&access_key=" + accessKeyCanary +
				"&ticket=" + ticketCanary
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"URL": websocketURL},
			})
		case "/ws":
			connection, err := (&websocket.Upgrader{}).Upgrade(w, request, nil)
			if err != nil {
				t.Errorf("upgrade websocket: %v", err)
				return
			}
			connected <- connection
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	os.Stdout = writePipe
	os.Stderr = writePipe
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		_ = writePipe.Close()
		_ = readPipe.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (serviceim.Consumer{
			AppID:     "cli_test",
			AppSecret: "redacted-test-app-secret",
			BaseURL:   server.URL,
		}).Consume(ctx, func(serviceim.EventEnvelope) error { return nil })
	}()

	var connection *websocket.Conn
	select {
	case connection = <-connected:
	case err := <-done:
		t.Fatalf("consumer exited before connection: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not connect")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	_ = connection.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not stop after cancellation")
	}

	os.Stdout = originalStdout
	os.Stderr = originalStderr
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{accessKeyCanary, ticketCanary} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("SDK log exposed credential canary %q:\n%s", forbidden, output)
		}
	}
}
