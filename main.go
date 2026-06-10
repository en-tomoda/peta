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
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
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
	mux.HandleFunc("GET /", application.handleIndex)
	mux.HandleFunc("POST /upload", application.handleUpload)
	mux.HandleFunc("GET /history", application.handleHistory)
	mux.HandleFunc("GET /share/{id}", application.handleShare)
	mux.HandleFunc("GET /content/{id}", application.handleContent)

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
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS pages (
    id TEXT PRIMARY KEY,
    title TEXT,
    html TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`)
	return err
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
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
	items, err := a.listHistory(r.Context(), maxHistory)
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
INSERT INTO pages(id, title, html)
VALUES(?, ?, ?)
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

func (a *app) listHistory(ctx context.Context, limit int) ([]historyItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, `
SELECT id, title, created_at
FROM pages
ORDER BY datetime(created_at) DESC, rowid DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]historyItem, 0, limit)
	for rows.Next() {
		var item historyItem
		var title sql.NullString
		if err := rows.Scan(&item.ID, &title, &item.CreatedAt); err != nil {
			return nil, err
		}
		if title.Valid {
			item.Title = title.String
		}
		item.URL = "/share/" + item.ID
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
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
