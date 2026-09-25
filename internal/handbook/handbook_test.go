package handbook

import (
	"strings"
	"testing"
)

func TestSnapshotAndOfflineSearch(t *testing.T) {
	if len(List()) < 20 {
		t.Fatal("expected complete upstream syntax handbook")
	}
	page, body, err := Read("flowchart")
	if err != nil || page.Version != "12.0.0" || !strings.Contains(page.Source, "mermaid%4012.0.0") || !strings.Contains(body, "flowchart LR") {
		t.Fatalf("snapshot metadata/content: %+v %v", page, err)
	}
	hits := Search("flowchart", 3)
	if len(hits) == 0 || hits[0].Slug != "flowchart" || len(hits) > 3 {
		t.Fatalf("unexpected search ranking: %+v", hits)
	}
	if hits := Search("unlikelywordthatcannotoccur", 20); len(hits) != 0 {
		t.Fatalf("expected no matches: %+v", hits)
	}
	if !strings.Contains(License(), "MIT License") {
		t.Fatal("missing upstream license")
	}
	if _, _, err := Read("../../etc/passwd"); err == nil {
		t.Fatal("unknown page must be rejected")
	}
}
