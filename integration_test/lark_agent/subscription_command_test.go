package larkagent_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSubscriptionCommandsPersistBaseURL(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	url := "https://example.larksuite.com/base/basExampleAppToken001?table=tblExampleTable001&view=vewExampleView001"
	code, stdout, stderr := runAgent(t, bin, "--state", state, "subscription", "add", url)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"app_token":"basExampleAppToken001"`) ||
		!strings.Contains(stdout, `"view_id":"vewExampleView001"`) ||
		!strings.Contains(stdout, `"monitor_modes":["base_record","base_field","cloud_docs_notice"]`) {
		t.Fatalf("add stdout=%s", stdout)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "subscription", "list")
	if code != 0 || !strings.Contains(stdout, `"subscriptions"`) {
		t.Fatalf("list exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "subscription", "inspect", url)
	if code != 0 || !strings.Contains(stdout, `"table_id":"tblExampleTable001"`) {
		t.Fatalf("inspect exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgent(t, bin, "--state", state, "subscription", "remove", url)
	if code != 0 || !strings.Contains(stdout, `"status":"removed"`) {
		t.Fatalf("remove exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestSubscriptionCommandsPersistDocumentURL(t *testing.T) {
	bin := buildAgentBinary(t)
	state := filepath.Join(t.TempDir(), "state.db")
	url := "https://example.larksuite.com/docx/DocTokenABC123"
	code, stdout, stderr := runAgent(t, bin, "--state", state, "subscription", "add", url)
	if code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"resource_type":"document"`) ||
		!strings.Contains(stdout, `"file_token":"DocTokenABC123"`) {
		t.Fatalf("add stdout=%s", stdout)
	}
}
