package lark

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCredentialSafeSDKLoggerRedactsWarningsAndErrors(t *testing.T) {
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

	logger := newCredentialSafeSDKLogger()
	logger.Info(context.Background(), "connected access_key=info-secret")
	logger.Warn(
		context.Background(),
		"reconnect wss://example.test/ws?access_key=query-secret&ticket=ticket-secret",
	)
	logger.Error(
		context.Background(),
		`bootstrap failed {"app_secret":"json-secret","refresh_token":"refresh-secret","tenant_access_token":"tenant-secret","client_assertion":"assertion-secret"}`,
	)

	os.Stdout = originalStdout
	os.Stderr = originalStderr
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	for _, forbidden := range []string{
		"info-secret",
		"query-secret",
		"ticket-secret",
		"json-secret",
		"refresh-secret",
		"tenant-secret",
		"assertion-secret",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("SDK logger exposed %q:\n%s", forbidden, text)
		}
	}
	for _, want := range []string{"reconnect", "bootstrap failed", "[REDACTED]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("SDK logger removed diagnostic context %q:\n%s", want, text)
		}
	}
}
