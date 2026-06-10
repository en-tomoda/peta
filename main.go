package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	dbPath       = "data/peta.db"
	legacyDBPath = "data/petabin.db"
	maxHTMLLen   = 2 * 1024 * 1024
	maxHistory   = 50
)

type app struct {
	db *sql.DB
}

type uploadRequest struct {
	Title string `json:"title"`
	HTML  string `json:"html"`
}

type uploadResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type historyItem struct {
	ID        string  `json:"id"`
	Title     string  `json:"title,omitempty"`
	FolderID  *string `json:"folder_id,omitempty"`
	URL       string  `json:"url"`
	CreatedAt string  `json:"created_at"`
}

type folderItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	CreatedAt string `json:"created_at"`
}

type createFolderRequest struct {
	Name string `json:"name"`
}

type assignFolderRequest struct {
	FolderID *string `json:"folder_id"`
}

func main() {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}
	if err := migrateLegacyDBFile(); err != nil {
		log.Fatalf("failed to migrate legacy db file: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	if err := migrate(db); err != nil {
		log.Fatalf("failed to migrate sqlite: %v", err)
	}

	application := &app{db: db}
	mux := http.NewServeMux()
	mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("/", application.handleIndex)
	mux.HandleFunc("POST /upload", application.handleUpload)
	mux.HandleFunc("GET /history", application.handleHistory)
	mux.HandleFunc("GET /share/{id}", application.handleShare)
	mux.HandleFunc("GET /content/{id}", application.handleContent)
	mux.HandleFunc("DELETE /page/{id}", application.handleDelete)
	mux.HandleFunc("GET /folders", application.handleGetFolders)
	mux.HandleFunc("POST /folders", application.handleCreateFolder)
	mux.HandleFunc("DELETE /folder/{id}", application.handleDeleteFolder)
	mux.HandleFunc("PATCH /page/{id}/folder", application.handleAssignFolder)
	mux.HandleFunc("POST /history/reorder", application.handleReorder)

	addr := ":8080"
	log.Printf("peta started at http://localhost%s", addr)
	if err := http.ListenAndServe(addr, withLogging(mux)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func migrateLegacyDBFile() error {
	if _, err := os.Stat(dbPath); err == nil {
		return nil
	}

	if _, err := os.Stat(legacyDBPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	return os.Rename(legacyDBPath, dbPath)
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS pages (
    id TEXT PRIMARY KEY,
    title TEXT,
    html TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`); err != nil {
		return err
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS folders (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`); err != nil {
		return err
	}
	// Idempotent: ignore error if column already exists
	db.Exec(`ALTER TABLE pages ADD COLUMN folder_id TEXT REFERENCES folders(id)`)
	db.Exec(`ALTER TABLE pages ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`)
	// Seed sort_order from rowid so existing items keep their insertion order
	db.Exec(`UPDATE pages SET sort_order = rowid WHERE sort_order = 0`)
	return nil
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "web/index.html")
}

func (a *app) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTMLLen+1024)
	defer r.Body.Close()

	req, err := parseUploadRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	req.HTML = strings.TrimSpace(req.HTML)
	if req.HTML == "" {
		http.Error(w, "html is required", http.StatusBadRequest)
		return
	}

	id, err := newID()
	if err != nil {
		http.Error(w, "failed to generate id", http.StatusInternalServerError)
		return
	}

	if err := a.insertPage(r.Context(), id, req.Title, req.HTML); err != nil {
		http.Error(w, "failed to save html", http.StatusInternalServerError)
		return
	}

	resp := uploadResponse{
		ID:  id,
		URL: "/share/" + id,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func parseUploadRequest(r *http.Request) (uploadRequest, error) {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseMultipartUploadRequest(r)
	}
	if strings.HasPrefix(contentType, "application/json") || contentType == "" {
		return parseJSONUploadRequest(r)
	}
	return uploadRequest{}, errors.New("unsupported content type")
}

func parseJSONUploadRequest(r *http.Request) (uploadRequest, error) {
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return uploadRequest{}, errors.New("invalid json")
	}
	return req, nil
}

func parseMultipartUploadRequest(r *http.Request) (uploadRequest, error) {
	if err := r.ParseMultipartForm(maxHTMLLen + 1024); err != nil {
		return uploadRequest{}, errors.New("invalid multipart form")
	}

	html := strings.TrimSpace(r.FormValue("html"))
	title := strings.TrimSpace(r.FormValue("title"))
	if html != "" {
		return uploadRequest{
			Title: title,
			HTML:  html,
		}, nil
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return uploadRequest{}, errors.New("html file is required")
	}
	defer file.Close()

	if !isHTMLFileName(header.Filename) {
		return uploadRequest{}, errors.New("only .html or .htm file is allowed")
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxHTMLLen+1))
	if err != nil {
		return uploadRequest{}, errors.New("failed to read uploaded file")
	}
	if len(raw) > maxHTMLLen {
		return uploadRequest{}, errors.New("uploaded html is too large")
	}

	return uploadRequest{
		Title: header.Filename,
		HTML:  string(raw),
	}, nil
}

func isHTMLFileName(name string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	return ext == ".html" || ext == ".htm"
}

func (a *app) handleShare(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	if _, err := a.getHTMLByID(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load page", http.StatusInternalServerError)
		return
	}

	tpl := template.Must(template.ParseFiles("web/share.html"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.Execute(w, struct {
		ID string
	}{
		ID: id,
	}); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
		return
	}
}

func (a *app) handleHistory(w http.ResponseWriter, r *http.Request) {
	folderFilter := r.URL.Query().Get("folder_id")
	sortKey      := r.URL.Query().Get("sort")
	items, err := a.listHistory(r.Context(), maxHistory, folderFilter, sortKey)
	if err != nil {
		http.Error(w, "failed to load history", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func (a *app) handleContent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}

	html, err := a.getHTMLByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "failed to load html", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write([]byte(html)); err != nil {
		http.Error(w, "failed to write html", http.StatusInternalServerError)
		return
	}
}

func (a *app) insertPage(ctx context.Context, id, title, html string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := a.db.ExecContext(ctx, `
INSERT INTO pages(id, title, html, sort_order)
VALUES(?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM pages))
`, id, title, html)
	return err
}

func (a *app) getHTMLByID(ctx context.Context, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var html string
	err := a.db.QueryRowContext(ctx, `
SELECT html
FROM pages
WHERE id = ?
`, id).Scan(&html)
	return html, err
}

func (a *app) listHistory(ctx context.Context, limit int, folderFilter, sortKey string) ([]historyItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	order := "sort_order DESC, rowid DESC"
	switch sortKey {
	case "asc":
		order = "datetime(created_at) ASC, rowid ASC"
	case "title":
		order = "LOWER(COALESCE(NULLIF(title,''), id)) ASC"
	}

	var (
		query string
		args  []any
	)
	switch folderFilter {
	case "__none__":
		query = `SELECT id, title, folder_id, created_at FROM pages WHERE folder_id IS NULL ORDER BY ` + order + ` LIMIT ?`
		args = []any{limit}
	case "":
		query = `SELECT id, title, folder_id, created_at FROM pages ORDER BY ` + order + ` LIMIT ?`
		args = []any{limit}
	default:
		query = `SELECT id, title, folder_id, created_at FROM pages WHERE folder_id = ? ORDER BY ` + order + ` LIMIT ?`
		args = []any{folderFilter, limit}
	}

	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]historyItem, 0, limit)
	for rows.Next() {
		var item historyItem
		var title, folderID sql.NullString
		if err := rows.Scan(&item.ID, &title, &folderID, &item.CreatedAt); err != nil {
			return nil, err
		}
		if title.Valid {
			item.Title = title.String
		}
		if folderID.Valid {
			item.FolderID = &folderID.String
		}
		item.URL = "/share/" + item.ID
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *app) handleGetFolders(w http.ResponseWriter, r *http.Request) {
	items, err := a.listFolders(r.Context())
	if err != nil {
		http.Error(w, "failed to load folders", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (a *app) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req createFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	id, err := newID()
	if err != nil {
		http.Error(w, "failed to generate id", http.StatusInternalServerError)
		return
	}
	if err := a.createFolder(r.Context(), id, strings.TrimSpace(req.Name)); err != nil {
		http.Error(w, "failed to create folder", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(folderItem{ID: id, Name: strings.TrimSpace(req.Name)})
}

func (a *app) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if err := a.deleteFolder(r.Context(), id); err != nil {
		http.Error(w, "failed to delete folder", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleAssignFolder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	var req assignFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := a.assignFolder(r.Context(), id, req.FolderID); err != nil {
		http.Error(w, "failed to assign folder", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if err := a.deletePage(r.Context(), id); err != nil {
		http.Error(w, "failed to delete page", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleReorder(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil || len(ids) == 0 {
		http.Error(w, "ids array required", http.StatusBadRequest)
		return
	}
	if err := a.reorderPages(r.Context(), ids); err != nil {
		http.Error(w, "failed to reorder", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) reorderPages(ctx context.Context, ids []string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE pages SET sort_order = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// ids[0] = top of list → highest sort_order so DESC query returns it first
	total := len(ids)
	for i, id := range ids {
		if _, err := stmt.ExecContext(ctx, total-i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *app) deletePage(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `DELETE FROM pages WHERE id = ?`, id)
	return err
}

func (a *app) listFolders(ctx context.Context) ([]folderItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `
SELECT f.id, f.name, f.created_at, COUNT(p.id)
FROM folders f LEFT JOIN pages p ON p.folder_id = f.id
GROUP BY f.id ORDER BY f.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []folderItem
	for rows.Next() {
		var f folderItem
		if err := rows.Scan(&f.ID, &f.Name, &f.CreatedAt, &f.Count); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

func (a *app) createFolder(ctx context.Context, id, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `INSERT INTO folders(id, name) VALUES(?, ?)`, id, name)
	return err
}

func (a *app) deleteFolder(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := a.db.ExecContext(ctx, `UPDATE pages SET folder_id = NULL WHERE folder_id = ?`, id); err != nil {
		return err
	}
	_, err := a.db.ExecContext(ctx, `DELETE FROM folders WHERE id = ?`, id)
	return err
}

func (a *app) assignFolder(ctx context.Context, pageID string, folderID *string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `UPDATE pages SET folder_id = ? WHERE id = ?`, folderID, pageID)
	return err
}

func newID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random read: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}
