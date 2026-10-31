# ASF Panel

[日本語](README.md) · [中文](README.zh.md) · **English**

A small web panel for ASF (ArchiSteamFarm), for you and your friends.

- **Playtime**: add several games at once, idle them forever or for a set time, and see what every bot is running
- **Card farming**: check progress, start or stop farming, manage games to skip
- **Bots**: create bots from the panel and sign in with a QR code or password; friends only see their own bots
- **Three languages**: 日本語 · 中文 · English

## Install

You need a server with Docker, a running ASF container named `asf`, and a domain pointing at the server.

**1. Download**

```bash
git clone https://github.com/kisukri/asf-ui.git
cd asf-ui
cp .env.example .env
```

**2. Let the panel reach ASF**

```bash
docker network create asf-net
docker network connect asf-net asf
```

**3. Generate a key**

```bash
docker compose run --rm asf-panel gen-key
```

Paste the printed line after `CREDENTIAL_KEYS=` in `.env`, and set `PUBLIC_ORIGIN` to your domain.

> Back this key up somewhere safe. Without it, the ASF passwords stored in the panel can't be decrypted.

**4. Start it and create an admin**

```bash
docker compose up -d --build
docker compose exec asf-panel /asf-panel create-admin admin
```

**5. Set up Caddy**

```caddyfile
panel.example.com {
    reverse_proxy 127.0.0.1:8000
}
```

```bash
sudo systemctl reload caddy
```

Open `https://your-domain` and sign in.

## Usage

1. **Add ASF**: "ASF connections" → address `http://asf:1242`, password is the one you use for the ASF web UI.
2. **Invite friends**: "Admin → Invites" → generate a code and send it.
3. **Create a bot**: "Admin → Bot assignment → New bot", sign in by QR or password, and assign it to a friend right away.
4. **Let friends create bots**: press "Let friends create bots here" under "ASF connections", then set their quota in "Admin → Users".
   They press "New bot" on the Playtime page and scan the QR code with the Steam app.
5. **Idle games**: enter App IDs or store links on the Playtime page (several at once). A duration of 0 means no limit.
6. **Manage a bot**: the "Manage" button on each bot under "Now idling" lets you add or remove games, set the online status (online, offline, invisible, …) and the text friends see.

Switch the language at the bottom of the sidebar or in Settings.

## Handy commands

| What | Command |
|---|---|
| Update | `git pull && docker compose up -d --build` |
| Logs | `docker compose logs -f` |
| Restart | `docker compose restart` |
| New invite code | `docker compose exec asf-panel /asf-panel create-invite` |
| Back up the database | `docker compose stop && docker compose cp asf-panel:/data ./backup && docker compose start` |

## FAQ

### "Request origin not allowed" when signing in

`PUBLIC_ORIGIN` in `.env` must start with `https://` and have no trailing `/`. Then run `docker compose up -d`.

### The connection says "Wrong or missing IPC password"

Use the same password as ASF's `IPCPassword`. After a few wrong tries ASF bans the IP for a while, so wait a few minutes.

### The connection says "Unreachable" or "Refused by ASF"

- Run `docker network connect asf-net asf` from step 2 again (needed after recreating the ASF container).
- ASF needs an `IPCPassword`; without one it only accepts local connections.

### No QR option when creating a bot

QR sign-in doesn't work when ASF runs headless. Set `"Headless"` to `false` in ASF's `config/ASF.json` and restart ASF.

### `docker compose up` says `undefined network asf-net`

Run `docker network create asf-net` from step 2 first.

## Options

Add to `.env`:

| Variable | Default | What it does |
|---|---|---|
| `ALLOW_USER_CONNECTIONS` | `false` | Let friends add their own ASF |
| `SESSION_IDLE_MINUTES` | `720` | Sign out after this many idle minutes |
| `SESSION_ABSOLUTE_HOURS` | `168` | Longest a session can last (hours) |
| `STEAM_NAME_LOOKUP` | `true` | Look up game names on the Steam store |
| `LOG_LEVEL` | `INFO` | Log level |

## Good to know

- Playtime games are written to the bot's ASF config (`GamesPlayedWhileIdle`), so they keep running through ASF restarts and panel downtime. When a task ends, only games the panel added are removed.
- ASF farms cards first and idles playtime games afterwards. To idle first, tick "Pause farming and start now" when adding.
- Playtime is the panel's estimate, not the number Steam shows.
- When you create a bot with a password, it's passed to ASF and stored in ASF's bot config (same as adding it in ASF). The panel doesn't keep it.
- "Stop farming" is undone when ASF restarts.
- Skip lists are per bot.
- The panel server holds your ASF password, so look after the server and `.env`.

## Without Docker

Needs Go 1.26+:

```bash
go build -o asf-panel .
./asf-panel gen-key          # put it in .env
./asf-panel create-admin admin
./asf-panel                  # listens on 127.0.0.1:8000
```

For local testing over plain http, add `COOKIE_SECURE=false` to `.env`.

## License

[MIT](LICENSE) © kisukri
