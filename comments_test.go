package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mustComment はコメントを1件作って、そのIDを返す。
func mustComment(t *testing.T, a *app, pageID, id string, req createCommentRequest) string {
	t.Helper()

	body, err := normalizeBody(req.Body)
	if err != nil {
		t.Fatalf("normalizeBody(%q): %v", req.Body, err)
	}
	if err := a.insertComment(context.Background(), id, pageID, req, body); err != nil {
		t.Fatalf("insertComment(%s): %v", id, err)
	}
	return id
}

func TestInsertCommentNumbersThreadsPerPage(t *testing.T) {
	a := newTestApp(t)
	seedPage(t, a, "pageA")
	seedPage(t, a, "pageB")

	anchor := &commentAnchor{Selector: "body", RX: 0.5, RY: 0.5, PX: 10, PY: 20}
	mustComment(t, a, "pageA", "a1", createCommentRequest{Author: "田中", Body: "1件目", Anchor: anchor})
	a2 := mustComment(t, a, "pageA", "a2", createCommentRequest{Author: "田中", Body: "2件目", Anchor: anchor})
	mustComment(t, a, "pageA", "a3", createCommentRequest{ParentID: &a2, Author: "佐藤", Body: "返信"})
	mustComment(t, a, "pageB", "b1", createCommentRequest{Author: "田中", Body: "別ページの1件目", Anchor: anchor})

	threadsA, err := a.listComments(context.Background(), "pageA")
	if err != nil {
		t.Fatalf("listComments: %v", err)
	}
	if len(threadsA) != 2 {
		t.Fatalf("pageA のスレッド数 = %d, want 2", len(threadsA))
	}
	if threadsA[0].Seq != 1 || threadsA[1].Seq != 2 {
		t.Errorf("seq = %d, %d, want 1, 2", threadsA[0].Seq, threadsA[1].Seq)
	}
	// 返信は番号を持たず、親の下にぶら下がる
	if len(threadsA[1].Replies) != 1 || threadsA[1].Replies[0].Seq != 0 {
		t.Errorf("返信が親にぶら下がっていない: %+v", threadsA[1].Replies)
	}

	// 番号はページごとに振り直す
	threadsB, err := a.listComments(context.Background(), "pageB")
	if err != nil {
		t.Fatalf("listComments: %v", err)
	}
	if len(threadsB) != 1 || threadsB[0].Seq != 1 {
		t.Errorf("pageB の seq = %+v, want 1件で seq=1", threadsB)
	}
}

func TestInsertCommentRejectsInvalidParent(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	seedPage(t, a, "pageB")

	root := mustComment(t, a, "pageA", "a1", createCommentRequest{Author: "田中", Body: "親", Anchor: &commentAnchor{Selector: "body"}})
	reply := mustComment(t, a, "pageA", "a2", createCommentRequest{ParentID: &root, Author: "佐藤", Body: "返信"})
	missing := "ないID"

	tests := []struct {
		name   string
		pageID string
		parent string
	}{
		{"返信への返信", "pageA", reply},
		{"別ページの親", "pageB", root},
		{"存在しない親", "pageA", missing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.parent
			err := a.insertComment(ctx, "new", tt.pageID, createCommentRequest{ParentID: &parent, Author: "x", Body: "y"}, "y")
			if !errors.Is(err, errInvalidParent) {
				t.Errorf("err = %v, want errInvalidParent", err)
			}
		})
	}
}

func TestToggleReaction(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	root := mustComment(t, a, "pageA", "a1", createCommentRequest{Author: "田中", Body: "本文", Anchor: &commentAnchor{Selector: "body"}})

	// 同じ人が同じ絵文字を2回押すと取り消しになる
	for i := 0; i < 3; i++ {
		if err := a.toggleReaction(ctx, root, "👍", "田中"); err != nil {
			t.Fatalf("toggleReaction: %v", err)
		}
	}
	if err := a.toggleReaction(ctx, root, "👍", "佐藤"); err != nil {
		t.Fatalf("toggleReaction: %v", err)
	}
	// 並び順は reactionEmojis の順に揃える（押した順ではない）
	if err := a.toggleReaction(ctx, root, "✅", "佐藤"); err != nil {
		t.Fatalf("toggleReaction: %v", err)
	}

	threads, err := a.listComments(ctx, "pageA")
	if err != nil {
		t.Fatalf("listComments: %v", err)
	}
	got := threads[0].Reactions
	if len(got) != 2 {
		t.Fatalf("スタンプの種類 = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Emoji != "👍" || got[0].Count != 2 {
		t.Errorf("1つ目 = %+v, want 👍 x2", got[0])
	}
	if strings.Join(got[0].Authors, ",") != "田中,佐藤" {
		t.Errorf("押した人 = %v, want [田中 佐藤]", got[0].Authors)
	}
	if got[1].Emoji != "✅" || got[1].Count != 1 {
		t.Errorf("2つ目 = %+v, want ✅ x1", got[1])
	}
}

func TestSetResolvedOnlyAffectsThreads(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	root := mustComment(t, a, "pageA", "a1", createCommentRequest{Author: "田中", Body: "親", Anchor: &commentAnchor{Selector: "body"}})
	reply := mustComment(t, a, "pageA", "a2", createCommentRequest{ParentID: &root, Author: "佐藤", Body: "返信"})

	if err := a.setResolved(ctx, root, true, "田中"); err != nil {
		t.Fatalf("setResolved: %v", err)
	}
	// 返信を解決しようとしても何も起きない
	if err := a.setResolved(ctx, reply, true, "佐藤"); err != nil {
		t.Fatalf("setResolved(reply): %v", err)
	}

	threads, _ := a.listComments(ctx, "pageA")
	if threads[0].ResolvedAt == nil {
		t.Error("親が未解決のまま")
	}
	if threads[0].ResolvedBy == nil || *threads[0].ResolvedBy != "田中" {
		t.Errorf("resolved_by = %v, want 田中", threads[0].ResolvedBy)
	}
	if threads[0].Replies[0].ResolvedAt != nil {
		t.Error("返信に解決状態が付いている")
	}

	if err := a.setResolved(ctx, root, false, ""); err != nil {
		t.Fatalf("setResolved(false): %v", err)
	}
	threads, _ = a.listComments(ctx, "pageA")
	if threads[0].ResolvedAt != nil || threads[0].ResolvedBy != nil {
		t.Errorf("未解決に戻っていない: %v / %v", threads[0].ResolvedAt, threads[0].ResolvedBy)
	}
}

func TestUpdateAnchorDoesNotMarkEdited(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")
	root := mustComment(t, a, "pageA", "a1", createCommentRequest{
		Author: "田中", Body: "本文",
		Anchor: &commentAnchor{Selector: "body>p:nth-of-type(1)", RX: 0.1, RY: 0.2, PX: 30, PY: 40},
	})

	moved := commentAnchor{Selector: "body>div:nth-of-type(3)", RX: 0.8, RY: 0.4, PX: 600, PY: 430}
	if err := a.updateAnchor(ctx, root, moved); err != nil {
		t.Fatalf("updateAnchor: %v", err)
	}

	threads, _ := a.listComments(ctx, "pageA")
	if *threads[0].Anchor != moved {
		t.Errorf("anchor = %+v, want %+v", *threads[0].Anchor, moved)
	}
	// ピンの移動は本文の変更ではないので「編集済み」にしない
	if threads[0].UpdatedAt != nil {
		t.Errorf("updated_at = %v, want nil", *threads[0].UpdatedAt)
	}

	if err := a.updateComment(ctx, root, "書き直した"); err != nil {
		t.Fatalf("updateComment: %v", err)
	}
	threads, _ = a.listComments(ctx, "pageA")
	if threads[0].UpdatedAt == nil {
		t.Error("本文を編集しても updated_at が入らない")
	}
}

func TestDeleteCommentCascadesToReplies(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	seedPage(t, a, "pageA")

	root := mustComment(t, a, "pageA", "a1", createCommentRequest{Author: "田中", Body: "親", Anchor: &commentAnchor{Selector: "body"}})
	reply := mustComment(t, a, "pageA", "a2", createCommentRequest{ParentID: &root, Author: "佐藤", Body: "返信"})
	kept := mustComment(t, a, "pageA", "a3", createCommentRequest{Author: "田中", Body: "残る", Anchor: &commentAnchor{Selector: "body"}})

	for _, id := range []string{root, reply, kept} {
		if err := a.toggleReaction(ctx, id, "👍", "田中"); err != nil {
			t.Fatalf("toggleReaction: %v", err)
		}
	}

	if err := a.deleteComment(ctx, root); err != nil {
		t.Fatalf("deleteComment: %v", err)
	}

	threads, _ := a.listComments(ctx, "pageA")
	if len(threads) != 1 || threads[0].ID != kept {
		t.Fatalf("残ったスレッド = %+v, want %s のみ", threads, kept)
	}
	// 親に付いていた番号は振り直さない（欠番を許容する）
	if threads[0].Seq != 2 {
		t.Errorf("seq = %d, want 2（欠番のまま）", threads[0].Seq)
	}
	if got := countRows(t, a, `SELECT COUNT(*) FROM comment_reactions`); got != 1 {
		t.Errorf("スタンプ = %d件, want 1", got)
	}
}

func TestNormalizeBody(t *testing.T) {
	if _, err := normalizeBody("   "); err == nil {
		t.Error("空の本文が通ってしまう")
	}
	if _, err := normalizeBody(strings.Repeat("あ", maxCommentBodyLen+1)); err == nil {
		t.Error("長すぎる本文が通ってしまう")
	}
	if _, err := normalizeBody(strings.Repeat("あ", maxCommentBodyLen)); err != nil {
		t.Errorf("上限ちょうどが弾かれた: %v", err)
	}
	got, err := normalizeBody("  前後の空白  ")
	if err != nil || got != "前後の空白" {
		t.Errorf("normalizeBody = %q, %v", got, err)
	}
}

func TestNormalizeAuthor(t *testing.T) {
	// マルチバイトでも文字数で切る
	long := strings.Repeat("あ", maxAuthorLen+10)
	if got := []rune(normalizeAuthor(long)); len(got) != maxAuthorLen {
		t.Errorf("長さ = %d, want %d", len(got), maxAuthorLen)
	}
	if got := normalizeAuthor("  田中  "); got != "田中" {
		t.Errorf("normalizeAuthor = %q", got)
	}
}

func TestIsAllowedEmoji(t *testing.T) {
	for _, emoji := range reactionEmojis {
		if !isAllowedEmoji(emoji) {
			t.Errorf("isAllowedEmoji(%q) = false", emoji)
		}
	}
	for _, emoji := range []string{"💩", "", "👍👍", "a"} {
		if isAllowedEmoji(emoji) {
			t.Errorf("isAllowedEmoji(%q) = true", emoji)
		}
	}
}
