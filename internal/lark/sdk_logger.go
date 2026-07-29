package lark

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

var (
	sdkQueryCredentialPattern = regexp.MustCompile(
		`(?i)((?:[?&]|\b)(?:access_key|ticket|authorization|client_assertion|[a-z_]*token|[a-z_]*secret)=)[^&\s\],}"']+`,
	)
	sdkJSONCredentialPattern = regexp.MustCompile(
		`(?i)("(?:access_key|ticket|authorization|client_assertion|[a-z_]*token|[a-z_]*secret)"\s*:\s*")[^"]*(")`,
	)
)

type credentialSafeSDKLogger struct {
	output *log.Logger
}

func newCredentialSafeSDKLogger() larkcore.Logger {
	return credentialSafeSDKLogger{
		output: log.New(os.Stdout, "", log.Ldate|log.Lmicroseconds),
	}
}

func (credentialSafeSDKLogger) Debug(context.Context, ...interface{}) {}

func (credentialSafeSDKLogger) Info(context.Context, ...interface{}) {}

func (logger credentialSafeSDKLogger) Warn(_ context.Context, args ...interface{}) {
	logger.write("Warn", args...)
}

func (logger credentialSafeSDKLogger) Error(_ context.Context, args ...interface{}) {
	logger.write("Error", args...)
}

func (logger credentialSafeSDKLogger) write(level string, args ...interface{}) {
	parts := make([]string, len(args))
	for index, arg := range args {
		parts[index] = sanitizeSDKLogText(fmt.Sprint(arg))
	}
	logger.output.Printf("[%s] %s", level, strings.Join(parts, " "))
}

func sanitizeSDKLogText(value string) string {
	value = sdkQueryCredentialPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return sdkJSONCredentialPattern.ReplaceAllString(value, `${1}[REDACTED]${2}`)
}
