# Peta

![Version](https://img.shields.io/badge/version-v0.2.0-blue)
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
- `iframe sandbox` 経由の表示
- 発行済み共有リンク履歴の表示（最新50件）
- 履歴アイテムの削除
- 履歴のドラッグ&ドロップ並べ替え
- フォルダ機能（作成・削除・アイテムの割り当て・フォルダ別フィルタ）
- 共有ページのヘッダー表示／非表示トグル（状態はブラウザに保存）
- ライト／ダークテーマ切り替え（状態はブラウザに保存）

## 使い方（Quick Start）

```bash
go mod tidy
go run .
```

ブラウザで [http://localhost:7382](http://localhost:7382) を開き、HTMLをアップロードすると共有リンクが発行されます。

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

発行済み共有リンク履歴（最新50件）を返します。`folder_id` クエリパラメータでフォルダ絞り込みが可能です（`__none__` を指定するとフォルダ未割り当てのみ）。

```json
[
  {
    "id": "abc123...",
    "title": "sample.html",
    "folder_id": "def456...",
    "url": "/share/abc123...",
    "created_at": "2026-06-11 00:00:00"
  }
]
```

### `DELETE /page/:id`

履歴からページを削除します。

### `POST /history/reorder`

履歴の並び順を更新します。ボディにIDの配列を渡すと、その順序で保存されます。

```json
["abc123", "def456", "ghi789"]
```

### `GET /folders`

フォルダ一覧とそれぞれのアイテム数を返します。

### `POST /folders`

フォルダを新規作成します。

```json
{ "name": "フォルダ名" }
```

### `DELETE /folder/:id`

フォルダを削除します。そのフォルダに割り当てられたページの割り当ては解除されます。

### `PATCH /page/:id/folder`

ページをフォルダに割り当てます。`folder_id` に `null` を指定すると割り当てを解除します。

```json
{ "folder_id": "def456..." }
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
│   ├── index.css
│   ├── share.html
│   ├── share.css
│   └── favicon.svg
├── data/
│   └── .gitkeep
└── README.md
```

## デプロイ（systemd）

Goアプリの一般的な運用として、`systemd` で常駐化します。

### 1. ビルドと配置

```bash
cd /path/to/peta
go build -o peta .

sudo mkdir -p /opt/peta
sudo cp peta /opt/peta/
sudo cp -r web /opt/peta/
sudo mkdir -p /opt/peta/data
```

### 2. serviceファイル作成

`/etc/systemd/system/peta.service`

```ini
[Unit]
Description=Peta Go App
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/peta
ExecStart=/opt/peta/peta
Restart=always
RestartSec=3
User=www-data
Group=www-data

[Install]
WantedBy=multi-user.target
```

### 3. 起動・自動起動

```bash
sudo systemctl daemon-reload
sudo systemctl enable peta
sudo systemctl start peta
sudo systemctl status peta
```

### 4. ログ確認

```bash
journalctl -u peta -f
```

## 運用メモ

- SQLite実DB（`data/peta.db`）は `.gitignore` で除外
- `data/.gitkeep` はディレクトリ維持のためにコミット
