package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	maxCommentBodyLen = 2000
	maxAuthorLen      = 40
)

// スタンプは5種固定。任意の文字列がDBに入らないようサーバー側でも制限する。
var reactionEmojis = []string{"👍", "✅", "👀", "🎉", "❓"}

type commentAnchor struct {
	Selector string  `json:"selector"`
	RX       float64 `json:"rx"`
	RY       float64 `json:"ry"`
	PX       float64 `json:"px"`
	PY       float64 `json:"py"`
}

type commentReaction struct {
	Emoji   string   `json:"emoji"`
	Count   int      `json:"count"`
	Authors []string `json:"authors"`
}

type commentItem struct {
	ID         string            `json:"id"`
	ParentID   *string           `json:"parent_id,omitempty"`
	Seq        int               `json:"seq"`
	Author     string            `json:"author"`
	Body       string            `json:"body"`
	Anchor     *commentAnchor    `json:"anchor,omitempty"`
	Reactions  []commentReaction `json:"reactions"`
	Replies    []commentItem     `json:"replies,omitempty"`
	ResolvedAt *string           `json:"resolved_at"`
	ResolvedBy *string           `json:"resolved_by,omitempty"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  *string           `json:"updated_at,omitempty"`
}

type createCommentRequest struct {
	ParentID *string        `json:"parent_id"`
	Author   string         `json:"author"`
	Body     string         `json:"body"`
	Anchor   *commentAnchor `json:"anchor"`
}

type updateCommentRequest struct {
	Body string `json:"body"`
}

type updateAnchorRequest struct {
	Anchor commentAnchor `json:"anchor"`
}

type reactionRequest struct {
	Emoji  string `json:"emoji"`
	Author string `json:"author"`
}

type resolveRequest struct {
	Resolved bool   `json:"resolved"`
	Author   string `json:"author"`
}

func migrateComments(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS comments (
    id              TEXT PRIMARY KEY,
    page_id         TEXT NOT NULL,
    parent_id       TEXT,
    seq             INTEGER NOT NULL DEFAULT 0,
    author          TEXT,
    body            TEXT NOT NULL,
    anchor_selector TEXT,
    anchor_rx       REAL,
    anchor_ry       REAL,
    anchor_px       REAL,
    anchor_py       REAL,
    resolved_at     DATETIME,
    resolved_by     TEXT,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME
);`); err != nil {
		return err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS comment_reactions (
    comment_id TEXT NOT NULL,
    emoji      TEXT NOT NULL,
    author     TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (comment_id, emoji, author)
);`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_comments_page ON comments(page_id)`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_comments_parent ON comments(parent_id)`)
	return err
}

func (a *app) handleGetComments(w http.ResponseWriter, r *http.Request) {
	pageID := strings.TrimSpace(r.PathValue("id"))
	if pageID == "" {
		http.NotFound(w, r)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	pageID := strings.TrimSpace(r.PathValue("id"))
	if pageID == "" {
		http.NotFound(w, r)
		return
	}

	var req createCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	body, err := normalizeBody(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	exists, err := a.pageExists(r.Context(), pageID)
	if err != nil {
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}

	id, err := newID()
	if err != nil {
		http.Error(w, "failed to generate id", http.StatusInternalServerError)
		return
	}

	if err := a.insertComment(r.Context(), id, pageID, req, body); err != nil {
		if errors.Is(err, errInvalidParent) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to save comment", http.StatusInternalServerError)
		return
	}

	a.respondComments(w, r, pageID)
}

func (a *app) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	var req updateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body, err := normalizeBody(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pageID, err := a.commentPageID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load comment", http.StatusInternalServerError)
		return
	}

	if err := a.updateComment(r.Context(), id, body); err != nil {
		http.Error(w, "failed to update comment", http.StatusInternalServerError)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) handleUpdateAnchor(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	var req updateAnchorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	pageID, err := a.commentPageID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load comment", http.StatusInternalServerError)
		return
	}

	if err := a.updateAnchor(r.Context(), id, req.Anchor); err != nil {
		http.Error(w, "failed to move comment", http.StatusInternalServerError)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	pageID, err := a.commentPageID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load comment", http.StatusInternalServerError)
		return
	}

	if err := a.deleteComment(r.Context(), id); err != nil {
		http.Error(w, "failed to delete comment", http.StatusInternalServerError)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) handleToggleReaction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	var req reactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !isAllowedEmoji(req.Emoji) {
		http.Error(w, "unsupported emoji", http.StatusBadRequest)
		return
	}
	author := normalizeAuthor(req.Author)
	if author == "" {
		http.Error(w, "author is required", http.StatusBadRequest)
		return
	}

	pageID, err := a.commentPageID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load comment", http.StatusInternalServerError)
		return
	}

	if err := a.toggleReaction(r.Context(), id, req.Emoji, author); err != nil {
		http.Error(w, "failed to toggle reaction", http.StatusInternalServerError)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) handleResolveComment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	pageID, err := a.commentPageID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load comment", http.StatusInternalServerError)
		return
	}

	if err := a.setResolved(r.Context(), id, req.Resolved, normalizeAuthor(req.Author)); err != nil {
		http.Error(w, "failed to update comment", http.StatusInternalServerError)
		return
	}
	a.respondComments(w, r, pageID)
}

func (a *app) respondComments(w http.ResponseWriter, r *http.Request, pageID string) {
	items, err := a.listComments(r.Context(), pageID)
	if err != nil {
		http.Error(w, "failed to load comments", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

var errInvalidParent = errors.New("parent comment not found")

func normalizeBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", errors.New("body is required")
	}
	if len([]rune(body)) > maxCommentBodyLen {
		return "", errors.New("body is too long")
	}
	return body, nil
}

func normalizeAuthor(author string) string {
	author = strings.TrimSpace(author)
	if runes := []rune(author); len(runes) > maxAuthorLen {
		return string(runes[:maxAuthorLen])
	}
	return author
}

func isAllowedEmoji(emoji string) bool {
	for _, e := range reactionEmojis {
		if e == emoji {
			return true
		}
	}
	return false
}

func (a *app) pageExists(ctx context.Context, pageID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var one int
	err := a.db.QueryRowContext(ctx, `SELECT 1 FROM pages WHERE id = ?`, pageID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (a *app) commentPageID(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var pageID string
	err := a.db.QueryRowContext(ctx, `SELECT page_id FROM comments WHERE id = ?`, id).Scan(&pageID)
	return pageID, err
}

func (a *app) insertComment(ctx context.Context, id, pageID string, req createCommentRequest, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	author := normalizeAuthor(req.Author)

	// 返信は1階層のみ。アンカーと番号は親コメントだけが持つ。
	if req.ParentID != nil && strings.TrimSpace(*req.ParentID) != "" {
		parentID := strings.TrimSpace(*req.ParentID)
		var parentPage string
		var parentOf sql.NullString
		err := a.db.QueryRowContext(ctx, `
SELECT page_id, parent_id FROM comments WHERE id = ?
`, parentID).Scan(&parentPage, &parentOf)
		if errors.Is(err, sql.ErrNoRows) {
			return errInvalidParent
		}
		if err != nil {
			return err
		}
		if parentPage != pageID || parentOf.Valid {
			return errInvalidParent
		}

		_, err = a.db.ExecContext(ctx, `
INSERT INTO comments(id, page_id, parent_id, seq, author, body)
VALUES(?, ?, ?, 0, ?, ?)
`, id, pageID, parentID, author, body)
		return err
	}

	var (
		selector sql.NullString
		rx, ry   sql.NullFloat64
		px, py   sql.NullFloat64
	)
	if req.Anchor != nil {
		if req.Anchor.Selector != "" {
			selector = sql.NullString{String: req.Anchor.Selector, Valid: true}
		}
		rx = sql.NullFloat64{Float64: req.Anchor.RX, Valid: true}
		ry = sql.NullFloat64{Float64: req.Anchor.RY, Valid: true}
		px = sql.NullFloat64{Float64: req.Anchor.PX, Valid: true}
		py = sql.NullFloat64{Float64: req.Anchor.PY, Valid: true}
	}

	_, err := a.db.ExecContext(ctx, `
INSERT INTO comments(id, page_id, parent_id, seq, author, body,
                     anchor_selector, anchor_rx, anchor_ry, anchor_px, anchor_py)
VALUES(?, ?, NULL,
       (SELECT COALESCE(MAX(seq), 0) + 1 FROM comments WHERE page_id = ? AND parent_id IS NULL),
       ?, ?, ?, ?, ?, ?, ?)
`, id, pageID, pageID, author, body, selector, rx, ry, px, py)
	return err
}

func (a *app) updateComment(ctx context.Context, id, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := a.db.ExecContext(ctx, `
UPDATE comments SET body = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
`, body, id)
	return err
}

// ピンの移動は本文の変更ではないので updated_at は触らない
func (a *app) updateAnchor(ctx context.Context, id string, anchor commentAnchor) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var selector sql.NullString
	if anchor.Selector != "" {
		selector = sql.NullString{String: anchor.Selector, Valid: true}
	}
	_, err := a.db.ExecContext(ctx, `
UPDATE comments
SET anchor_selector = ?, anchor_rx = ?, anchor_ry = ?, anchor_px = ?, anchor_py = ?
WHERE id = ? AND parent_id IS NULL
`, selector, anchor.RX, anchor.RY, anchor.PX, anchor.PY, id)
	return err
}

func (a *app) deleteComment(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 親を消すと、その返信とスタンプもまとめて消える
	if _, err := tx.ExecContext(ctx, `
DELETE FROM comment_reactions
WHERE comment_id = ? OR comment_id IN (SELECT id FROM comments WHERE parent_id = ?)
`, id, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM comments WHERE id = ? OR parent_id = ?
`, id, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *app) toggleReaction(ctx context.Context, commentID, emoji, author string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	res, err := a.db.ExecContext(ctx, `
DELETE FROM comment_reactions WHERE comment_id = ? AND emoji = ? AND author = ?
`, commentID, emoji, author)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}

	_, err = a.db.ExecContext(ctx, `
INSERT INTO comment_reactions(comment_id, emoji, author) VALUES(?, ?, ?)
`, commentID, emoji, author)
	return err
}

func (a *app) setResolved(ctx context.Context, id string, resolved bool, author string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// 解決状態を持つのは親コメントだけ
	if !resolved {
		_, err := a.db.ExecContext(ctx, `
UPDATE comments SET resolved_at = NULL, resolved_by = NULL
WHERE id = ? AND parent_id IS NULL
`, id)
		return err
	}
	_, err := a.db.ExecContext(ctx, `
UPDATE comments SET resolved_at = CURRENT_TIMESTAMP, resolved_by = ?
WHERE id = ? AND parent_id IS NULL
`, author, id)
	return err
}

func (a *app) listComments(ctx context.Context, pageID string) ([]commentItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	reactions, err := a.listReactions(ctx, pageID)
	if err != nil {
		return nil, err
	}

	rows, err := a.db.QueryContext(ctx, `
SELECT id, parent_id, seq, author, body,
       anchor_selector, anchor_rx, anchor_ry, anchor_px, anchor_py,
       resolved_at, resolved_by, created_at, updated_at
FROM comments
WHERE page_id = ?
ORDER BY datetime(created_at) ASC, rowid ASC
`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []commentItem
	for rows.Next() {
		var (
			item                   commentItem
			parentID               sql.NullString
			author                 sql.NullString
			selector               sql.NullString
			rx, ry, px, py         sql.NullFloat64
			resolvedAt, resolvedBy sql.NullString
			updatedAt              sql.NullString
		)
		if err := rows.Scan(
			&item.ID, &parentID, &item.Seq, &author, &item.Body,
			&selector, &rx, &ry, &px, &py,
			&resolvedAt, &resolvedBy, &item.CreatedAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		if parentID.Valid {
			item.ParentID = &parentID.String
		}
		if author.Valid {
			item.Author = author.String
		}
		if selector.Valid || px.Valid {
			item.Anchor = &commentAnchor{
				Selector: selector.String,
				RX:       rx.Float64,
				RY:       ry.Float64,
				PX:       px.Float64,
				PY:       py.Float64,
			}
		}
		if resolvedAt.Valid {
			item.ResolvedAt = &resolvedAt.String
		}
		if resolvedBy.Valid {
			item.ResolvedBy = &resolvedBy.String
		}
		if updatedAt.Valid {
			item.UpdatedAt = &updatedAt.String
		}
		item.Reactions = reactions[item.ID]
		if item.Reactions == nil {
			item.Reactions = []commentReaction{}
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	repliesByParent := make(map[string][]commentItem)
	for _, c := range all {
		if c.ParentID != nil {
			repliesByParent[*c.ParentID] = append(repliesByParent[*c.ParentID], c)
		}
	}

	threads := make([]commentItem, 0, len(all))
	for _, c := range all {
		if c.ParentID != nil {
			continue
		}
		c.Replies = repliesByParent[c.ID]
		threads = append(threads, c)
	}
	sort.SliceStable(threads, func(i, j int) bool { return threads[i].Seq < threads[j].Seq })
	return threads, nil
}

func (a *app) listReactions(ctx context.Context, pageID string) (map[string][]commentReaction, error) {
	rows, err := a.db.QueryContext(ctx, `
SELECT r.comment_id, r.emoji, r.author
FROM comment_reactions r
JOIN comments c ON c.id = r.comment_id
WHERE c.page_id = ?
ORDER BY datetime(r.created_at) ASC, r.rowid ASC
`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	authorsByComment := make(map[string]map[string][]string)
	for rows.Next() {
		var commentID, emoji, author string
		if err := rows.Scan(&commentID, &emoji, &author); err != nil {
			return nil, err
		}
		if authorsByComment[commentID] == nil {
			authorsByComment[commentID] = make(map[string][]string)
		}
		authorsByComment[commentID][emoji] = append(authorsByComment[commentID][emoji], author)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 絵文字の並びは reactionEmojis の順に固定する
	result := make(map[string][]commentReaction, len(authorsByComment))
	for commentID, byEmoji := range authorsByComment {
		list := make([]commentReaction, 0, len(byEmoji))
		for _, emoji := range reactionEmojis {
			authors := byEmoji[emoji]
			if len(authors) == 0 {
				continue
			}
			list = append(list, commentReaction{Emoji: emoji, Count: len(authors), Authors: authors})
		}
		result[commentID] = list
	}
	return result, nil
}

func (a *app) deletePageComments(ctx context.Context, tx *sql.Tx, pageID string) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM comment_reactions WHERE comment_id IN (SELECT id FROM comments WHERE page_id = ?)
`, pageID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE page_id = ?`, pageID)
	return err
}
