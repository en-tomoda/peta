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
- 指摘をプレビュー上の位置に紐づけて残せるので、「どこの話か」を説明せずに済む

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
- コメントレビュー
  - プレビュー上の任意の位置に番号付きピンでコメントを追加
  - ピンのドラッグ移動（落とした位置でアンカーを張り直し）
  - 右端のコメントパネル（開閉可、未解決のみ絞り込み）
  - スタンプ（`👍` `✅` `👀` `🎉` `❓`）と1階層の返信
  - 解決済みトグル、コメントの編集・削除
  - 履歴一覧へのコメント数バッジ表示

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

`allow-same-origin` を付けていないため、親ページから `iframe` の中は読めません。
コメントのピン位置を扱うために、`/content/:id` の配信時だけ
`web/content-agent.js` の読み込みを1行注入します。

- DBに保存されたHTMLは書き換えません（配信時のみ付加）
- 注入したスクリプトの役割は、クリック位置の通知とピン座標の算出・通知のみ
- 親との通信は `postMessage` に限定

ピンの位置は「アンカー要素（`nth-of-type` パス）＋要素内の相対位置」で保存するため、
ウィンドウ幅やパネル開閉でレイアウトが回り込んでも対象要素に追従します。
ページ内タブなどでアンカー要素が非表示になった場合は、ピンも隠れます。

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
    "created_at": "2026-06-11T00:00:00Z",
    "comment_count": 3,
    "open_count": 1
  }
]
```

`comment_count` は返信を含むコメント総数、`open_count` は未解決スレッド数です。

### `DELETE /page/:id`

履歴からページを削除します。そのページに付いたコメントとスタンプもまとめて削除されます。

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

### `GET /page/:id/comments`

ページのコメントをスレッド単位で返します。

```json
[
  {
    "id": "a1b2c3",
    "seq": 1,
    "author": "田中",
    "body": "ヘッダーの余白が広い",
    "anchor": { "selector": "body>div:nth-of-type(2)", "rx": 0.42, "ry": 0.31, "px": 540, "py": 220 },
    "reactions": [{ "emoji": "👍", "count": 2, "authors": ["田中", "佐藤"] }],
    "replies": [{ "id": "d4e5f6", "author": "佐藤", "body": "直します", "reactions": [], "created_at": "..." }],
    "resolved_at": null,
    "created_at": "2026-10-23T05:45:43Z"
  }
]
```

### `POST /page/:id/comments`

コメントを投稿します。`parent_id` を指定すると返信になります（返信は1階層のみ）。
返信にはアンカーと番号を持たせません。

```json
{
  "author": "田中",
  "body": "ヘッダーの余白が広い",
  "anchor": { "selector": "body>div:nth-of-type(2)", "rx": 0.42, "ry": 0.31, "px": 540, "py": 220 }
}
```

### `PATCH /comment/:id`

本文を編集します。`updated_at` が入り、画面では「編集済み」と表示されます。

```json
{ "body": "ヘッダーの余白が広すぎる" }
```

### `PATCH /comment/:id/anchor`

ピンの位置を変更します。本文の変更ではないため `updated_at` は更新しません。

```json
{ "anchor": { "selector": "body>p:nth-of-type(3)", "rx": 0.5, "ry": 0.5, "px": 300, "py": 880 } }
```

### `DELETE /comment/:id`

コメントを削除します。親を削除すると、その返信とスタンプもまとめて削除されます。

### `POST /comment/:id/reactions`

スタンプを付け外しします（同じ組み合わせをもう一度送ると取り消し）。
絵文字は `👍` `✅` `👀` `🎉` `❓` の5種のみ受け付けます。

```json
{ "emoji": "👍", "author": "田中" }
```

### `POST /comment/:id/resolve`

解決状態を切り替えます。親コメントのみが対象です。

```json
{ "resolved": true, "author": "田中" }
```

これらのAPIはいずれも、更新後のスレッド一覧（`GET /page/:id/comments` と同じ形）を返します。

## コメントレビューの制約

ログイン機構がないため、共有URLを知っている人は誰でも読み書き・削除できます。
投稿者名は自己申告で、ブラウザの `localStorage` に保存されます。
編集・削除のボタンは名前が一致するコメントにだけ表示されますが、サーバー側では検証していません。

リアルタイム同期は行いません。他の人のコメントは再読み込みで反映されます。
同じHTMLを再アップロードすると別のページ（別ID）になるため、コメントは引き継がれません。

## 技術スタック

- Backend: Go (`net/http`)
- Database: SQLite (`data/peta.db`)
- Frontend: HTML / CSS / Vanilla JavaScript

## ディレクトリ構成

```txt
peta/
├── .gitignore
├── go.mod
├── go.sum
├── main.go
├── comments.go
├── web/
│   ├── index.html
│   ├── index.css
│   ├── share.html
│   ├── share.css
│   ├── share.js
│   ├── content-agent.js
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
