package tools

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const maxPublicMessageUUIDNamespaceBytes = 17

// PublicMessageUUID converts an unbounded durable action key into a stable
// Lark message UUID while preserving a short action namespace for diagnostics.
func PublicMessageUUID(namespace, internalKey string) string {
	if internalKey == "" {
		return ""
	}
	namespace = strings.Trim(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		namespace = "message"
	}
	if len(namespace) > maxPublicMessageUUIDNamespaceBytes {
		namespaceDigest := sha256.Sum256([]byte(namespace))
		namespace = fmt.Sprintf("ns%x", namespaceDigest[:7])
	}
	digest := sha256.Sum256([]byte(internalKey))
	return fmt.Sprintf("%s:%x", namespace, digest[:16])
}
