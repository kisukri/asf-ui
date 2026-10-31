# ASF Panel

**日本語** · [中文](README.zh.md) · [English](README.en.md)

自分と友人のための ASF（ArchiSteamFarm）Web パネル。

- **プレイ時間**：複数のゲームをまとめて追加。無期限でも、時間を決めて自動停止でも。各 Bot がいま何を回しているかもひと目でわかります
- **カードファーム**：進捗の確認、ファームの開始・停止、スキップするゲームの設定
- **Bot 管理**：パネルから Bot を作成し、QR コードかパスワードでログイン。友人には自分の Bot だけが見えます
- **3 言語**：日本語・中文・English

## インストール

必要なもの：Docker が入ったサーバー、動いている ASF コンテナ（名前は `asf`）、サーバーを指すドメイン。

**1. ダウンロード**

```bash
git clone https://github.com/kisukri/asf-ui.git
cd asf-ui
cp .env.example .env
```

**2. パネルから ASF に届くようにする**

```bash
docker network create asf-net
docker network connect asf-net asf
```

**3. 鍵を作る**

```bash
docker compose run --rm asf-panel gen-key
```

出てきた 1 行を `.env` の `CREDENTIAL_KEYS=` の後ろに貼り、`PUBLIC_ORIGIN` を自分のドメインに変えます。

> この鍵は別に保管してください。なくすと、パネルに保存した ASF のパスワードが読めなくなります。

**4. 起動して管理者を作る**

```bash
docker compose up -d --build
docker compose exec asf-panel /asf-panel create-admin admin
```

**5. Caddy を設定**

```caddyfile
panel.example.com {
    reverse_proxy 127.0.0.1:8000
}
```

```bash
sudo systemctl reload caddy
```

`https://自分のドメイン` を開いてログインすれば完了です。

## 使い方

1. **ASF を追加**：「ASF 接続」→ アドレスは `http://asf:1242`、パスワードは ASF の Web 画面を開くときのもの。
2. **友人を招待**：「管理者 → 招待コード」で作ったコードを友人に送ります。
3. **Bot を作成**：「管理者 → Bot の割り当て → Bot を作成」。QR かパスワードでログインし、そのまま友人に割り当てられます。
4. **友人に Bot を作ってもらう**：「ASF 接続」で「友人の Bot 作成を許可」を押し、「管理者 → ユーザー」で作成枠を設定。
   友人は「プレイ時間」の「Bot を作成」から、スマホの Steam アプリで QR を読み取るだけです。
5. **プレイ時間**：「プレイ時間」に App ID かストアのリンクを入力（複数可）。時間を 0 にすると無期限です。
6. **Bot の管理**：「稼働中」の各 Bot の「管理」から、ゲームの追加・削除、オンライン状態（オンライン／オフライン／非表示など）、フレンドに見える表示テキストを設定できます。

画面の言語はサイドバー下部か「設定」で切り替えられます。

## よく使うコマンド

| やりたいこと | コマンド |
|---|---|
| 更新 | `git pull && docker compose up -d --build` |
| ログを見る | `docker compose logs -f` |
| 再起動 | `docker compose restart` |
| 招待コードを作る | `docker compose exec asf-panel /asf-panel create-invite` |
| データベースのバックアップ | `docker compose stop && docker compose cp asf-panel:/data ./backup && docker compose start` |

## よくある質問

### ログイン時に「このリクエスト元は許可されていません」と出る

`.env` の `PUBLIC_ORIGIN` は `https://` から書き、末尾に `/` を付けません。直したら `docker compose up -d`。

### 接続が「IPC パスワードが違うか未設定」になる

ASF の `IPCPassword` と同じパスワードにしてください。何度か間違えると ASF が一時的にブロックするので、数分待ってから再試行します。

### 接続が「接続できません」「ASF に拒否されました」になる

- 手順 2 の `docker network connect asf-net asf` をもう一度実行（ASF コンテナを作り直したあとは必要です）。
- ASF に `IPCPassword` が必要です。未設定だとローカルからしか接続できません。

### Bot 作成で QR コードが選べない

ASF が Headless モードだと QR ログインは使えません。ASF の `config/ASF.json` で `"Headless"` を `false` にして ASF を再起動してください。

### `docker compose up` で `undefined network asf-net` と出る

先に手順 2 の `docker network create asf-net` を実行してください。

## オプション設定

`.env` に追加します：

| 変数 | 既定値 | 内容 |
|---|---|---|
| `ALLOW_USER_CONNECTIONS` | `false` | 友人が自分の ASF を追加できるようにする |
| `SESSION_IDLE_MINUTES` | `720` | 操作がないと自動ログアウトするまでの分数 |
| `SESSION_ABSOLUTE_HOURS` | `168` | ログインの最長時間（時間） |
| `STEAM_NAME_LOOKUP` | `true` | Steam ストアからゲーム名を取得する |
| `LOG_LEVEL` | `INFO` | ログレベル |

## 知っておくこと

- プレイ時間のゲームは ASF の Bot 設定（`GamesPlayedWhileIdle`）に書き込むので、ASF の再起動やパネル停止中も回り続けます。タスク終了時に消すのは、パネルが追加したゲームだけです。
- ASF はカードファームを先に行い、終わってからプレイ時間のゲームを回します。先に回したいときは、追加時に「先にファームを止めて今すぐ開始」にチェック。
- プレイ時間はパネルの推定で、Steam に表示される時間ではありません。
- Bot 作成時の Steam パスワードは ASF に渡され、ASF の Bot 設定に保存されます（ASF で手動追加するのと同じ）。パネルには保存しません。
- 「ファーム停止」は ASF を再起動すると元に戻ります。
- スキップリストは Bot ごとに保存されます。
- パネルのサーバーは ASF のパスワードを持っています。サーバーと `.env` は大切に扱ってください。

## Docker を使わない場合

Go 1.26 以上が必要です：

```bash
go build -o asf-panel .
./asf-panel gen-key          # .env に貼る
./asf-panel create-admin admin
./asf-panel                  # 127.0.0.1:8000 で待ち受け
```

ローカルで http のまま試すときは、`.env` に `COOKIE_SECURE=false` を追加してください。

## ライセンス

[MIT](LICENSE) © kisukri
