# ASF Panel

[日本語](README.md) · **中文** · [English](README.en.md)

给自己和朋友用的 ASF（ArchiSteamFarm）网页面板。

- **游戏时长**：一次添加多个游戏，一直挂或到时自动停止；随时查看每个 Bot 正在挂什么
- **卡牌挂机**：查看进度，启动 / 停止挂卡，设置跳过的游戏
- **Bot 管理**：直接在面板里新建 Bot，扫码或账号密码登录；朋友只能看到和操作自己的 Bot
- **三种语言**：日本語 · 中文 · English

## 安装

需要：一台装了 Docker 的服务器、已经在运行的 ASF 容器（名字叫 `asf`）、一个指向服务器的域名。

**1. 下载**

```bash
git clone https://github.com/kisukri/asf-ui.git
cd asf-ui
cp .env.example .env
```

**2. 让面板能连上 ASF**

```bash
docker network create asf-net
docker network connect asf-net asf
```

**3. 生成密钥**

```bash
docker compose run --rm asf-panel gen-key
```

把输出的那一行填到 `.env` 的 `CREDENTIAL_KEYS=` 后面，再把 `PUBLIC_ORIGIN` 改成你的域名。

> 这个密钥要单独备份。丢了以后，面板里保存的 ASF 密码就解不开了。

**4. 启动并创建管理员**

```bash
docker compose up -d --build
docker compose exec asf-panel /asf-panel create-admin admin
```

**5. 配置 Caddy**

```caddyfile
panel.example.com {
    reverse_proxy 127.0.0.1:8000
}
```

```bash
sudo systemctl reload caddy
```

打开 `https://你的域名` 登录即可。

## 使用

1. **添加 ASF**：“ASF 连接” → 地址填 `http://asf:1242`，密码填打开 ASF 网页时输入的密码。
2. **邀请朋友**：“后台管理 → 邀请码” → 生成后发给朋友注册。
3. **新建 Bot**：“后台管理 → Bot 分配 → 新建 Bot”，选扫码或账号密码登录，可以直接分配给朋友。
4. **让朋友自己建 Bot**：“ASF 连接”里点“允许朋友在这里新建 Bot”，再到“后台管理 → 用户”给他设置自建额度。
   朋友在“游戏时长”页点“新建 Bot”，用手机 Steam App 扫码即可。
5. **挂时长**：“游戏时长”里填 App ID 或商店链接（可以一次填多个），时长填 0 就一直挂。
6. **管理 Bot**：“现在在挂”里每个 Bot 的“管理”按钮，可以直接增删挂时长的游戏、设置在线状态（在线 / 离线 / 隐身等）和好友看到的自定义文字。

界面语言可以在侧栏底部或“设置”里切换。

## 常用命令

| 做什么 | 命令 |
|---|---|
| 更新 | `git pull && docker compose up -d --build` |
| 看日志 | `docker compose logs -f` |
| 重启 | `docker compose restart` |
| 生成邀请码 | `docker compose exec asf-panel /asf-panel create-invite` |
| 备份数据库 | `docker compose stop && docker compose cp asf-panel:/data ./backup && docker compose start` |

## 常见问题

### 登录时提示“请求来源不被允许”

`.env` 里的 `PUBLIC_ORIGIN` 要带 `https://`，结尾不要有 `/`。改完执行 `docker compose up -d`。

### 连接显示“IPC 密码错误或缺失”

密码要和 ASF 的 `IPCPassword` 一致。连续输错几次 ASF 会临时封禁，等几分钟再试。

### 连接显示“无法连接”或“被 ASF 拒绝访问”

- 重新执行第 2 步的 `docker network connect asf-net asf`（重建过 ASF 容器后需要）。
- ASF 必须设置了 `IPCPassword`，没设密码时它只允许本机访问。

### 新建 Bot 时没有“扫码”选项

ASF 开启了 Headless（无头模式）时不支持扫码。在 ASF 的 `config/ASF.json` 里把 `"Headless"` 改成 `false`，重启 ASF 即可。

### `docker compose up` 报 `undefined network asf-net`

先执行第 2 步的 `docker network create asf-net`。

## 可选设置

在 `.env` 中添加：

| 变量 | 默认 | 作用 |
|---|---|---|
| `ALLOW_USER_CONNECTIONS` | `false` | 允许朋友添加自己的 ASF |
| `SESSION_IDLE_MINUTES` | `720` | 多久不操作自动退出（分钟） |
| `SESSION_ABSOLUTE_HOURS` | `168` | 最长登录时间（小时） |
| `STEAM_NAME_LOOKUP` | `true` | 从 Steam 商店查询游戏名称 |
| `LOG_LEVEL` | `INFO` | 日志级别 |

## 注意

- 挂时长的游戏写在 ASF 的 Bot 配置里（`GamesPlayedWhileIdle`），ASF 重启、面板停机都照常挂；任务结束时面板只删掉自己加的游戏。
- ASF 先挂卡，卡挂完才挂时长游戏；想先挂时长，添加时勾选“先暂停挂卡”。
- 游戏时长是面板估算的挂机时间，不是 Steam 显示的时长。
- 新建 Bot 时 Steam 密码会转发给 ASF，由 ASF 保存在它的 Bot 配置里（和在 ASF 里手动添加一样），面板不保存。
- “停止挂卡”在 ASF 重启后失效。
- 跳过列表按 Bot 保存。
- 面板服务器掌握 ASF 密码，请保护好服务器和 `.env`。

## 不用 Docker

需要 Go 1.26+：

```bash
go build -o asf-panel .
./asf-panel gen-key          # 填入 .env
./asf-panel create-admin admin
./asf-panel                  # 监听 127.0.0.1:8000
```

本机用 http 测试时，在 `.env` 里加 `COOKIE_SECURE=false`。

## 许可证

[MIT](LICENSE) © kisukri
