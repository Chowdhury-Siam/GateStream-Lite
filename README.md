<div align="center">
  <img src="bot/server/templates/brand.svg" width="88" alt="GateStream gateway logo">
  <h1>GateStream</h1>
  <p><strong>Gateway to your media.</strong></p>
  <p>Play and download your Telegram files.</p>
</div>

GateStream turns media in your Telegram channels into browser playback,
direct downloads, and API links. The bot manages your catalog in MongoDB;
the server retrieves media from Telegram as it streams. A dark interface,
warm gold accents, and a gateway mark carry the identity from bot to browser.

## GateStream-Lite v2.0.38

This repository contains the complete Python origin, Telegram bot, web interface,
Dockerfile, scripts and tests. Extract the source ZIP so these files sit directly
inside your project directory. Existing Cloudflare Workers continue to proxy this
origin; their deployment files are maintained in the main GateStream repository.

**Serv00 / FreeBSD:** follow [SERV00.md](SERV00.md) for installation, reserved ports,
SSL/reverse proxy, resource tuning and recovery. Other hosts can use the steps below.
See [CHANGELOG.md](CHANGELOG.md) for the configuration and shutdown fixes.

## What you can do

- **Save Telegram files.** Upload a file directly to get its own link.
- **Follow transfers live.** The home page shows active links, HTTP connections, byte counts, progress, speed, elapsed time and ETA.
- **Watch your way.** Play in the browser, resume playback, or open VLC or MX Player.
- **Share a link.** Create stream, download, and API URLs; revoke access by removing a link.
- **Maintain file records.** Inspect server stats, backfill durations, and back up or restore records.
- **Add an edge entry point.** Deploy the optional Cloudflare streaming reverse proxy from GitHub Actions.

Media remains in Telegram. Selected opening and ending chunks are cached on
the host's disk. Extra bot tokens support parallel retrieval; actual speed
depends on Telegram, your host, and the network. The Worker passes media through
without buffering the whole file; it does not create a distributed media cache.

## Quick start on a local machine or VPS

Use Python 3.11 or newer, a MongoDB endpoint reachable from the host, and a
Telegram bot created through BotFather. Give the bot access to your source channels.

```bash
git clone https://github.com/Chowdhury-Siam/GateStream-Lite.git
cd GateStream-Lite
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
cp .env.example .env
```

On Windows, activate with `.venv\Scripts\activate` and copy `.env.example` to `.env`.
Fill in the required values below, set `BASE_URL` to your public server or Worker
address, then start the bot:

```bash
python -m bot
```

Project repository: [Chowdhury-Siam/GateStream-Lite](https://github.com/Chowdhury-Siam/GateStream-Lite).
Run only one deployment per main Telegram bot token.

## Configuration

Use `.env` locally. Values come from system environment (including empty values),
then `.env`, legacy `config.env`, literal `start.sh` assignments, and finally defaults.
Comments never supply settings. Invalid numeric values fail validation; empty
optional worker tokens disable extras. No shell code is executed. All components
use `bot/config.py`. See [SERV00.md](SERV00.md) for exact parsing/empty-value rules.

On Hugging Face, use **Settings → Variables and secrets**;
do not upload a filled `.env` file.

| Setting | Required | Purpose |
| --- | --- | --- |
| `TELEGRAM_API_ID` | Yes | Numeric Telegram API ID |
| `TELEGRAM_API_HASH` | Yes | Telegram API hash |
| `TELEGRAM_BOT_TOKEN` | Yes | Main BotFather token |
| `OWNER_ID` | Yes | Your positive numeric Telegram user ID |
| `DATABASE_URL` | Yes for hosted deployments | MongoDB URI; local default is `mongodb://localhost:27017` |
| `BASE_URL` | Outside Spaces | Public URL used in generated links; Spaces otherwise uses `SPACE_HOST` |
| `DATABASE_NAME` | No | Catalog database; legacy default retained as `OmniArchiver` |
| `MULTI_BOT_TOKENS` | No | Additional bot tokens, separated by spaces or commas |
| `TELEGRAM_CHANNEL_ID` | No | Storage channel for uploads, first-start notices, and worker checks |
| `TELEGRAM_BOT_USERNAME` | No | Username fallback; detected from the bot when connected |
| `OMNI_MONGO_URI` | No | Compatibility setting for a separate upstream catalog |
| `PORT` | No | `8080` locally; `7860` in the Docker image and on Spaces |
| `BIND_ADDRESS` | No | Listening address; defaults to `0.0.0.0` |

### Public host and Cloudflare

On the bot host, `BASE_URL` is the address users should open. For your existing
Worker deployment:

```dotenv
BASE_URL=https://your-worker.example
```

In GitHub Actions settings, `CLOUDFLARE_ORIGIN_URL` is the **direct hosting
address**, not a `/stream/...` link and not the Worker URL. Example:

```text
https://your-origin.example
```

The main [GateStream repository](https://github.com/Chowdhury-Siam/GateStream)
maintains the Worker deployment workflow. Its origin setting accepts a repository
Variable or Secret; Cloudflare credentials belong in GitHub Secrets. Lite keeps
the origin HTTP routes compatible with that separately deployed Worker.

## Telegram access and commands

The bot starts with access for `OWNER_ID` only. The owner manages access with
`/auth` and `/unauth`. Grants are stored in the existing MongoDB database.

| Action | Command |
| --- | --- |
| Authorize a user | `/auth USER_ID` or reply to their message with `/auth` |
| Authorize a group | `/auth GROUP_ID` or send `/auth` in that group |
| Revoke a grant | Use `/unauth` with the same ID, reply, or group |
| Open the welcome menu | `/start` |
| Read commands and instructions | `/help` |

Use a positive Telegram ID for a user and a negative ID for a group.
### File To Link group topic

1. As the bot owner, open the **File To Link** topic and send `/filetolink`.
   This saves the group ID and topic ID. To select a different topic, run the
   same command there. If `/start` says the current topic differs from the
   saved one, the owner can rebind it by sending `/filetolink` there as a new
   message. A MongoDB read error is reported separately in the owner menu.
2. Use `/auth` in the topic to grant group access if the group is new to
   GateStream. Existing group and user grants apply.
3. Give the bot access to ordinary messages in the group. A bot that is a
   group admin receives them. If you disable group privacy in BotFather,
   re-add the bot to the group for the change to take effect.
4. Post a file in the topic to receive its link automatically. To request
   a link again, reply to one of your own topic files with `/link`.

Group uploads and group `/link` are scoped to that topic. Private uploads
require a direct user grant.

Owner tools include `/stats`, `/workers`, `/log`, `/sync_duration`,
`/set_title`, `/revoke`, `/clean`, `/backup`, `/restore`, `/purge`,
and `/restart`.

## Worker diagnostics

`/stats` shows connected bots, host resources, MongoDB library totals and live
transfers. `/workers` asks every connected bot to post one silent check message
to `TELEGRAM_CHANNEL_ID`. The private result lists only bots that could not
post, with a reason such as offline, posting permission denied, or timed out.
Use `/workers CHANNEL_ID` to send the check to a specific GateStream storage
channel. A bot needs posting permission in the selected channel to pass.

## HTTP endpoints

| Endpoint | Response |
| --- | --- |
| `/` | GateStream landing page |
| `/api/activity` | Live aggregate transfer counters and anonymous progress rows |
| `/health`, `/ping` | `OK` when the web server is responding; `X-GateStream-Version` identifies the deployed release |
| `/stream/<code>` | Browser player |
| `/dl/<code>` | Download; add `?stream=1` for inline media |
| `/api/file/<code>` | File details and links |
| `/api/batch/<code>` | Batch details and episode links |

Anyone holding a valid media URL can access it. Telegram user restrictions
apply to bot commands. Share media you have
permission to distribute.

## Docker

```bash
docker build -t gatestream .
docker run -d --name gatestream -p 8080:7860 --env-file .env -e PORT=7860 --restart unless-stopped gatestream
```

Set `BASE_URL` for the public address mapped to this container. For Spaces, retain your README YAML metadata (including `sdk: docker` and
`app_port: 7860`) and use the included Dockerfile. Configure credentials using
Space secrets. Keep the image port, Space port and platform environment consistent.

## Live activity dashboard

The home page shows the total number of individual files downloaded, with
live bandwidth cards in an expandable panel. A completion means GateStream
handed all file bytes to response streams. Overlapping ranges for the same
anonymous client and file are combined. HEAD and inline playback do not
increment download totals.

In-flight range coverage is kept in memory for up to 24 hours and 1024 anonymous
client/resource pairs (at most 256 disjoint ranges each). It resets on restart,
so a download resumed across a server restart may not count as a new completion.
Client grouping uses a non-public hash of connection address and user agent;
clients sharing both may be grouped. New range sequences starting at byte zero
after a ten-second completion grace can count again. These are server-side
delivery totals, not exact per-person analytics. Completed totals persist using
the same five-second MongoDB checkpoint as existing counters.

Open the server or Worker home page to follow transfers live. The dashboard
refreshes every two seconds while visible and slows polling in background tabs.
Media links are counted separately from HTTP range connections. It shows data
sent to viewers, completed requests, failures, incomplete transfers and server uptime.

Active rows show progress, transferred/total bytes, average speed, elapsed time
and ETA. Unknown-size downloads use an indeterminate progress bar. Summary
throughput covers roughly five seconds. Only the latest 20 active rows are shown;
all active transfers are included in the counters. On a network failure the
previous values stay dimmed and the panel shows disconnected until it reconnects.

The public endpoint exposes aggregate counts and anonymous activity numbers,
without file names, URLs, file codes, credentials or user IDs.
Cumulative bytes, completed counts and failed/incomplete counts are stored in
MongoDB `analytics`, document `transfer_activity`. They are restored at startup,
checkpointed every five seconds, and flushed during normal web shutdown and
`/restart`. Active connections, active rows, current speeds and uptime remain
live session values and correctly start at zero after a restart. Previously
active transfers from the saved checkpoint are counted as incomplete.

A hard kill or power loss can lose work since the last successful checkpoint
(normally up to five seconds); a database outage can extend that window. Failed
reads do not overwrite stored totals with zeros, failed writes are retried
without adding totals twice, and the dashboard shows a save/restore warning.
Chunks never wait for statistics database writes. Use the same `DATABASE_URL`
and `DATABASE_NAME` to preserve totals. This uses one writer per deployment;
it does not aggregate multiple replicas. Existing media bytes can be seeded
from the older `analytics.bandwidth` record on first upgrade. Historical
counts and other totals that were never saved cannot be reconstructed. The
media-only `/backup` export does not include these analytics records; a full
database backup does.

Media byte totals measure bytes handed to HTTP responses, not proof a client
saved a whole file. Failed/incomplete transfers retain their partial byte totals.
Legacy checkpoint categories are retained for compatibility; their historical
values are not erased and are not shown as active features on the home page.

## Upgrading an existing deployment

1. Back up the database with `/backup` and retain your current hosting settings.
2. Use a clean source directory from the appropriate ZIP so retired plugins are
   removed. Copy over your existing configuration and Telegram session files.
   Keep the media cache if you want to reuse it. Batch ZIP caches from older releases are retired.
3. Keep the current database names, bot tokens, and Worker name, then restart the host.
4. Test `/health`, generate a file link, and check playback and seeking.

This release retains MongoDB defaults
`OmniArchiver` and `OmniArchiverDB`, `OMNI_MONGO_URI`, single-file codes and routes,
Telegram session names, and the browser's existing resume keys. No database
migration is needed. Renaming storage merely for branding could hide existing
records, so these compatibility identifiers remain intentionally.

Existing Worker names and deployments are unchanged by this origin update. Manage
Worker deployment settings in the main GateStream repository; use the origin's
public reverse proxy URL and keep `BASE_URL` pointing at your public entry point.

## Checks

```bash
python -m unittest discover -s tests -v
```

Lite includes the Python suite. Worker JavaScript/Node tests belong to the separate
main repository and are needed when changing the edge Worker. Python tests use
mocked services; the shutdown integration tests bind real loopback HTTP sockets.
See [VALIDATION.md](VALIDATION.md) for release results and limits.

## Project history

GateStream is Siam's rebrand of OmniArchiver-F2L. See
[ACKNOWLEDGEMENTS.md](ACKNOWLEDGEMENTS.md) for upstream attribution and
[CHANGELOG.md](CHANGELOG.md) for recent changes.
