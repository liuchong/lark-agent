package domain

import "testing"

func TestModeParsing(t *testing.T) {
	for _, mode := range []Mode{ModeAuto, ModeApproval, ModePaused} {
		got, err := ParseMode(string(mode))
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", mode, err)
		}
		if got != mode {
			t.Fatalf("ParseMode(%q)=%q", mode, got)
		}
	}
	if _, err := ParseMode("manual"); err == nil {
		t.Fatal("ParseMode accepted invalid mode")
	}
}

func TestWorkItemFromEventUsesMessageIDAsDedupKey(t *testing.T) {
	ev := NormalizedEvent{
		Source:    SourceRealtime,
		EventID:   "evt-1",
		MessageID: "om_1",
		ChatID:    "oc_1",
		SenderID:  "ou_sender",
		Content:   "ping",
		Mentions:  []Mention{{OpenID: "ou_owner"}},
	}
	item := NewWorkItem(ev)
	if item.DedupKey != "message:om_1" {
		t.Fatalf("DedupKey=%q", item.DedupKey)
	}
	if item.Status != StatusReceived {
		t.Fatalf("Status=%q", item.Status)
	}
	if !item.Event.MentionsUser("ou_owner") {
		t.Fatal("expected mention match")
	}
}
