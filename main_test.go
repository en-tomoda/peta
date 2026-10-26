package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestApp(t *testing.T) *app {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &app{db: db}
}

func seedPage(t *testing.T, a *app, id string) {
	t.Helper()
	if err := a.insertPage(context.Background(), id, id+".html", "<h1>"+id+"</h1>"); err != nil {
		t.Fatalf("insertPage(%s): %v", id, err)
	}
}

func TestInjectContentAgent(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "closing body tag",
			html: "<html><body><h1>hi</h1></body></html>",
			want: "<html><body><h1>hi</h1>" + contentAgentTag + "</body></html>",
		},
		{
			name: "uppercase closing body tag",
			html: "<HTML><BODY><p>x</p></BODY></HTML>",
			want: "<HTML><BODY><p>x</p>" + contentAgentTag + "</BODY></HTML>",
		},
		{
			name: "html tag only",
			html: "<html><p>x</p></html>",
			want: "<html><p>x</p>" + contentAgentTag + "</html>",
		},
		{
			name: "fragment",
			html: "<div>only a fragment</div>",
			want: "<div>only a fragment</div>" + contentAgentTag,
		},
		{
			name: "unclosed tags",
			html: "<html><body><p>unclosed",
			want: "<html><body><p>unclosed" + contentAgentTag,
		},
		{
			name: "last closing body tag wins",
			html: "<body>a</body><!-- </body> --></body>",
			want: "<body>a</body><!-- </body> -->" + contentAgentTag + "</body>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := injectContentAgent(tt.html); got != tt.want {
				t.Errorf("injectContentAgent()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestIsHTMLFileName(t *testing.T) {
	allowed := []string{"a.html", "a.htm", "A.HTML", " spaced.html ", "dir.name/file.htm"}
	for _, name := range allowed {
		if !isHTMLFileName(name) {
			t.Errorf("isHTMLFileName(%q) = false, want true", name)
		}
	}

	denied := []string{"a.txt", "a.html.js", "a", "a.svg", ""}
	for _, name := range denied {
		if isHTMLFileName(name) {
			t.Errorf("isHTMLFileName(%q) = true, want false", name)
		}
	}
}

func TestListHistoryCountsComments(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	seedPage(t, a, "pageB")

	// pageA: 未解決1 + 解決済み1、解決済みのほうに返信1
	root1 := mustComment(t, a, "pageA", "c1", createCommentRequest{Author: "田中", Body: "未解決", Anchor: &commentAnchor{Selector: "body"}})
	root2 := mustComment(t, a, "pageA", "c2", createCommentRequest{Author: "田中", Body: "解決する", Anchor: &commentAnchor{Selector: "body"}})
	mustComment(t, a, "pageA", "c3", createCommentRequest{ParentID: &root2, Author: "佐藤", Body: "返信"})
	if err := a.setResolved(ctx, root2, true, "田中"); err != nil {
		t.Fatalf("setResolved: %v", err)
	}
	_ = root1

	items, err := a.listHistory(ctx, maxHistory, "", "")
	if err != nil {
		t.Fatalf("listHistory: %v", err)
	}

	counts := map[string][2]int{}
	for _, item := range items {
		counts[item.ID] = [2]int{item.CommentCount, item.OpenCount}
	}

	// comment_count は返信込みの総数、open_count は未解決スレッド数
	if got, want := counts["pageA"], [2]int{3, 1}; got != want {
		t.Errorf("pageA counts = %v, want %v", got, want)
	}
	if got, want := counts["pageB"], [2]int{0, 0}; got != want {
		t.Errorf("pageB counts = %v, want %v", got, want)
	}
}

func TestDeletePageDeletesComments(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	seedPage(t, a, "pageB")

	root := mustComment(t, a, "pageA", "c1", createCommentRequest{Author: "田中", Body: "消える", Anchor: &commentAnchor{Selector: "body"}})
	mustComment(t, a, "pageA", "c2", createCommentRequest{ParentID: &root, Author: "佐藤", Body: "返信も消える"})
	if err := a.toggleReaction(ctx, root, "👍", "田中"); err != nil {
		t.Fatalf("toggleReaction: %v", err)
	}
	mustComment(t, a, "pageB", "c3", createCommentRequest{Author: "田中", Body: "残る", Anchor: &commentAnchor{Selector: "body"}})

	if err := a.deletePage(ctx, "pageA"); err != nil {
		t.Fatalf("deletePage: %v", err)
	}

	if got := countRows(t, a, `SELECT COUNT(*) FROM comments WHERE page_id = ?`, "pageA"); got != 0 {
		t.Errorf("pageA のコメントが %d件 残っている", got)
	}
	if got := countRows(t, a, `SELECT COUNT(*) FROM comment_reactions`); got != 0 {
		t.Errorf("スタンプが %d件 残っている", got)
	}
	if got := countRows(t, a, `SELECT COUNT(*) FROM comments WHERE page_id = ?`, "pageB"); got != 1 {
		t.Errorf("pageB のコメント = %d件, want 1", got)
	}
}

func countRows(t *testing.T, a *app, query string, args ...any) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
