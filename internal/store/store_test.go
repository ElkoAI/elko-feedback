package store

import (
	"path/filepath"
	"testing"
)

func TestChannelIDsAndPrefix(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	for _, bad := range []string{"", "Elko", "elko..x", ".elko", "elko/x", "elko.x."} {
		if err := st.UpsertChannel(&Channel{ID: bad, Title: "x"}); err == nil {
			t.Errorf("accepted bad id %q", bad)
		}
	}
	for _, id := range []string{"elko.mcp.news", "elko.mcp.newsroom", "elko.mcp", "other"} {
		if err := st.UpsertChannel(&Channel{ID: id, Title: id, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertFeedback(&Feedback{Channel: id, Message: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	// prefix "elko.mcp.news" must not match "elko.mcp.newsroom"
	if _, n, _ := st.ListFeedback(Filter{Prefix: "elko.mcp.news"}); n != 1 {
		t.Fatalf("prefix boundary: %d", n)
	}
	if _, n, _ := st.ListFeedback(Filter{Prefix: "elko"}); n != 3 {
		t.Fatalf("prefix elko: %d", n)
	}
	if st.IPHash("1.2.3.4") == st.IPHash("1.2.3.5") || len(st.IPHash("1.2.3.4")) != 32 {
		t.Fatal("ip hash")
	}
	if err := st.SetSetting("ip_salt", "x"); err == nil {
		t.Fatal("ip_salt editable")
	}
	if err := st.CreateAdmin("a", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}
