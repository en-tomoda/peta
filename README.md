# Peta

![Version](https://img.shields.io/badge/version-v0.1.0-blue)
![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go)
![SQLite](https://img.shields.io/badge/SQLite-3-003B57?logo=sqlite)

HTMLをすぐに共有できる、社内向けの軽量プレビューツール
貼り付け or ファイルアップロードしたHTMLから共有URLを発行し、安全な `iframe` 表示でレビューできます。

## Why Peta

- LPレビューやHTMLモック共有を、最短1分で開始できる
- サーバー構成はシンプル（Go + SQLite 1ファイル）
- アップロードHTMLは `iframe sandbox` で隔離して表示
- 発行済みリンク履歴が残るので、再確認がしやすい

## 主な機能

- HTMLテキスト貼り付けアップロード
- `.html` / `.htm` ファイルアップロード
- 共有URL発行（`/share/:id`）
- 共有ページ上部にプレビューヘッダー表示（`Peta Preview` をクリックでホームへ）
- `iframe sandbox` 経由の表示
- 発行済み共有リンク履歴の表示（最新50件）

## 使い方（Quick Start）

```bash
go mod tidy
go run .
```

ブラウザで [http://localhost:8080](http://localhost:8080) を開き、HTMLをアップロードすると共有リンクが発行されます。

## プレビュー表示の仕組み

`/share/:id` で直接HTMLを描画せず、次の構成で表示します。

`/share/:id` -> `iframe` -> `/content/:id`

- `sandbox="allow-scripts allow-forms"` を適用
- `allow-same-origin` / `allow-top-navigation` / `allow-popups` は不許可

## API

### `POST /upload`

HTMLを保存し、共有URLを返します。

Request (JSON):

```json
{
  "html": "<h1>Hello</h1>"
}
```

Request (multipart/form-data):

- `file`: `.html` または `.htm` ファイル
- または `html`: HTML文字列

Response:

```json
{
  "id": "abc123...",
  "url": "/share/abc123..."
}
```

### `GET /share/:id`

共有用ページ（ヘッダー + `iframe`）を返します。

### `GET /content/:id`

保存済みHTMLを `text/html` で返します。

### `GET /history`

発行済み共有リンク履歴（最新50件）を返します。

```json
[
  {
    "id": "abc123...",
    "title": "sample.html",
    "url": "/share/abc123...",
    "created_at": "2026-06-11 00:00:00"
  }
]
```

## 技術スタック

- Backend: Go (`net/http`)
- Database: SQLite (`data/peta.db`)
- Frontend: HTML / CSS / Vanilla JavaScript

## ディレクトリ構成

```txt
peta/
├── .gitignore
├── main.go
├── go.mod
├── go.sum
├── web/
│   ├── index.html
│   └── share.html
├── data/
│   └── .gitkeep
└── README.md
```

## 運用メモ

- SQLite実DB（`data/peta.db`）は `.gitignore` で除外
- `data/.gitkeep` はディレクトリ維持のためにコミット
