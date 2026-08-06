package investigation

import "testing"

func TestPublicMessageUUIDIsStableBoundedAndStageDistinct(t *testing.T) {
	ownerKey := "message:om_example_message_002:investigation-owner-notice"
	progressKey := "message:om_example_message_002:investigation-progress"

	first := publicMessageUUID(ownerKey)
	if first != publicMessageUUID(ownerKey) {
		t.Fatalf("public message UUID is not stable: %q", first)
	}
	if len(first) > 50 {
		t.Fatalf("public message UUID length=%d key=%q", len(first), first)
	}
	if first == publicMessageUUID(progressKey) {
		t.Fatalf("owner notice and progress share public UUID %q", first)
	}
	if first == ownerKey {
		t.Fatalf("full internal action key leaked to Lark: %q", first)
	}
}
