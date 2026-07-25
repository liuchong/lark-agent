package lark

import "testing"

func TestParseBaseResourceURL(t *testing.T) {
	ref, err := ParseResourceURL("https://example.larksuite.com/base/basExampleAppToken001?table=tblExampleTable001&view=vewExampleView001")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ResourceType != ResourceTypeBase ||
		ref.AppToken != "basExampleAppToken001" ||
		ref.TableID != "tblExampleTable001" ||
		ref.ViewID != "vewExampleView001" {
		t.Fatalf("ref=%+v", ref)
	}
}

func TestParseWikiResourceURLKeepsNodeUntilRuntimeResolution(t *testing.T) {
	ref, err := ParseResourceURL("https://example.larksuite.com/wiki/wikExampleNodeToken001?table=blkExampleEmbeddedView001")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ResourceType != ResourceTypeWiki ||
		ref.WikiNodeToken != "wikExampleNodeToken001" ||
		ref.AppToken != "" ||
		ref.TableID != "blkExampleEmbeddedView001" {
		t.Fatalf("ref=%+v", ref)
	}
}

func TestParseDocumentResourceURL(t *testing.T) {
	ref, err := ParseResourceURL("https://example.larksuite.com/docx/DocTokenABC123")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ResourceType != ResourceTypeDocument ||
		ref.FileToken != "DocTokenABC123" ||
		ref.AppToken != "" ||
		ref.WikiNodeToken != "" {
		t.Fatalf("ref=%+v", ref)
	}
}
