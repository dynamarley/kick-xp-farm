# kick-level-farm

A Windows and Linux CLI for one Kick account, adapted from the existing
`kick-bot` channel discovery and WebSocket workflow. The application interface
is in English. No Node.js installation or browser is required.

## Getting started

Windows (PowerShell):

```powershell
.\dist\kick-level-farm-windows-amd64.exe
```

Linux (x86_64):

```sh
chmod +x ./dist/kick-level-farm-linux-amd64
./dist/kick-level-farm-linux-amd64
```

On first launch, enter your Kick account's **Bearer access token**. You can paste
it with or without the `Bearer ` prefix. Token input is visible while typing
or pasting. The CLI verifies your
account with Kick before starting and saves credentials only after verification
succeeds. Rejected tokens are requested again. Saved credentials are verified
on each launch without asking you to re-enter them. You do not need to enter
channel names. The dashboard displays the verified account's username.

The CLI watches **one stream at a time** and offers three watching modes:

- **Following first** (default): live followed channels, then discovery.
- **Category first**: streams in the selected category, then live followed
  channels, then discovery. Both fallback sources may be outside the category.
- **Priority channels first**: the ordered channel list, then live followed
  channels, then discovery.

Explicit priority channels override every mode, including category mode, in
their saved order. Following first and Priority channels first therefore use
the same selection order when explicit priorities are set. No channel name is
required. When a stream ends, the selection is refreshed immediately; another
live followed channel is chosen before discovery. Stream and higher-tier
availability checks run every **30 seconds**. The bot returns to a higher tier
when one becomes available, while streams in the same tier do not interrupt
each other. Empty scans retry after 30 seconds. API latency can delay switching.
WebSocket heartbeats continue independently of API scans; watch tracking
messages retain their two-minute interval.

Discovery uses `https://kick.com/stream/livestreams/en`; `en` is the interface
locale, and results include multiple stream languages. Each scan fetches the
first three pages (currently up to 32 results per page). Priority channels are
checked directly even when they are outside those pages.
Category mode scans three category-filtered pages separately, using Kick's
`subcategory` filter. Enter the slug from the category's Kick URL, for example
`just-chatting` or `grand-theft-auto-v`. If following cannot be loaded, the bot
reports the failure and continues with discovery.

## Daily watch rewards

Automatic daily reward collection is **enabled by default**. While the bot is
running, it checks Kick's gamification API at startup and every minute. When
Kick marks a daily watch reward `claimable`, the CLI sends the same claim
request as the website. It includes daily event boxes that use watch-time
goals. Other reward types and one-off challenges are left untouched.

Overview shows the watch progress reported by Kick (for example `42 / 60 min`),
collection status, or an API error. Successful collections appear in the bot
log. Local session uptime does not establish eligibility. The server decides
when watch time qualifies and which reward is awarded.

Collected boxes are remembered for their server-provided daily window, so
stale responses do not trigger another claim. The server's window also handles
the next day's reset, independent of your local timezone or channel changes.
After restarting, the API's `claimed` status prevents collecting again. Failed
or uncertain requests are followed by a fresh status check before another
attempt, with increasing retry delays and respect for HTTP 429 Retry-After.
Reward errors keep the watcher running and never display credentials.

Use **Settings → `g`** to toggle daily collection, or:

```sh
kick-level-farm settings --daily-rewards=false
kick-level-farm settings --daily-rewards=true
```

Keep the application running to collect automatically each day. Pausing or
quitting stops both watching and collection; the next start checks for ready
rewards immediately. Reward collection requires an authenticated account Bearer
token even in viewer mode. No extra credential prompt is added.

API paths observed in Kick's web client:
`GET https://web.kick.com/api/v1/gamification/challenges` and
`POST https://web.kick.com/api/v1/gamification/challenges/{challenge_id}/claim`.

## Commands

To use the original script's viewer-token and WebSocket workflow without
requiring account identity verification:

```sh
kick-level-farm run --viewer
```

Enter your Bearer token. Extra session cookies are not requested when the
viewer connection succeeds. If needed, add `--with-session` to allow optional
cookie prompts after a rejected Bearer-only connection.
The CLI checks the viewer-token request and opens a WebSocket before saving
settings and starting discovery. The dashboard shows **ACCOUNT UNVERIFIED**
alongside the live connection status; this mode does not verify account sign-in
or account credit. The selected mode and credentials are saved, so later
launches only need `kick-level-farm`. Use `login --viewer=false` to switch back
to account verification. `--with-session` also works with `run` and `dashboard`.

These examples use `kick-level-farm` as the executable name. Substitute
`.\dist\kick-level-farm-windows-amd64.exe` on Windows or
`./dist/kick-level-farm-linux-amd64` on Linux.

```sh
kick-level-farm
kick-level-farm dashboard
kick-level-farm run --plain
kick-level-farm settings
kick-level-farm settings --priority channel1,channel2
kick-level-farm settings --clear-priority
kick-level-farm settings --mode following
kick-level-farm settings --mode category --category just-chatting
kick-level-farm settings --mode priority --priority channel1,channel2
kick-level-farm channels
kick-level-farm login
kick-level-farm login --with-session
kick-level-farm status
kick-level-farm logout
kick-level-farm --help
```

- `run` or no command: discover live channels and start watching automatically.
- `dashboard`: explicitly open the full-screen terminal dashboard.
- `run --plain`: use line-based logs instead of the dashboard.
- `settings`: choose watching mode, category, and priorities in the Settings tab,
  with the bot initially paused. Without an
  interactive terminal, use the numbered settings menu.
- `settings --priority`: save channel names in priority order, left to right.
  Use names rather than URLs. Offline channels can be added too.
- `settings --clear-priority`: clear the list and use automatic selection.
- `settings --mode following|category|priority`: save a watching mode. Category
  mode requires a category; use `--category just-chatting` in the same command.
  These settings commands work without signing in.
- `channels`: fetch the live directory without signing in and show the first
  20 channels with viewer counts, languages, and titles.
- `login`: verify and save or replace the token while keeping channel priorities.
- `login --with-session`: test Bearer alone, then compare with optional
  `XSRF-TOKEN`, `kick_session`, and `kick_session_id` cookie values. All credential
  inputs are visible while typing or pasting. Leave optional values blank to skip them. Paste only each value,
  without its cookie name or attributes. Verified cookies are saved and sent
  with account, channel, viewer-token, and WebSocket requests. The XSRF cookie
  is preserved and its URL-decoded value is used in `X-XSRF-TOKEN`.
- `status`: show whether a token is saved, channel priorities, and the config
  path. This command does not display the token or call the API.
- `logout`: remove the saved token and all session cookies while keeping priorities. This does not
  revoke the Kick session on the server.

Changes made in the dashboard apply immediately. Settings commands run from a
separate terminal apply on the next launch. Channel lists saved by the first
CLI version are migrated to priorities, preserving the saved token.

## Dashboard

Normal startup opens a full-screen dashboard in Windows Terminal and Linux
terminals. The top-right indicator shows **Discovering**, **Connecting**,
**Watching**, **Waiting**, **Stopping**, **Paused**, or an error. **Watching** is
shown only after the WebSocket connects and the initial messages are sent.

- **Overview**: current channel, connection, channel time, session uptime, last
  send time, message count, and channel priorities.
- **Live**: navigate with arrow keys; press Enter to add or remove a favorite.
  Press `r` to refresh the list.
- **Following** (`4`): shows your followed channels, including offline channels.
  Live channels appear first, with viewer counts and categories. The list loads
  automatically using your saved account Bearer token, including all cursor
  pages from `/api/v2/channels/followed-page`. Press `r` to refresh and Enter to
  add or remove the selected channel from priorities. This changes local
  priorities; it does not follow or unfollow channels on Kick. If Kick rejects
  the account token, the tab shows an error while the bot keeps running.
- **Settings**: press `m` to cycle watching modes, `c` to edit the category and
  enable category mode, `g` to toggle daily reward collection, `a` to add channels, `d` to remove the selected channel,
  and Ctrl+Up/Down to reorder priorities. The current mode, category, and
  fallback order are displayed. Changes are saved and applied immediately.
- **Bot log**: recent connection and watching events. PgUp/PgDn scroll through
  history; End returns to the latest events.

Tab or 1/2/3/4 switches tabs. `b` pauses or resumes the bot. `l` logs out: it removes
the locally saved Bearer token and all session cookies, closes the connection,
and exits. Channel priorities are kept; the next launch requests a token again.
Log out is also shown in Settings and the dashboard footer. `q` or Ctrl+C closes
the connection, restores the terminal, and exits. Applying updated preferences
waits for the previous connection to close before starting another worker.
Login and logout preserve the watching mode, category, and channel priorities.

The Settings command starts with the bot paused; press `b` to start it. The
dashboard requires at least 50 columns and 18 rows. Smaller terminals display
a resize hint and the current status.

The interface uses Kick green, cyan, and lavender, with amber warnings and pink
errors. Redirecting output to a file or pipe uses line-based logs without
full-screen control codes.

```sh
kick-level-farm status --color always
kick-level-farm channels --color never
```

`NO_COLOR` disables automatic colors. `--color always` forces colors. Under
`TERM=dumb`, line-based output is the default; in an ANSI-capable terminal,
`dashboard` or `--color always` can explicitly enable the dashboard.

Account verification requires a positive account ID and a username; HTTP 200
with an empty object is inconclusive. In that case, the CLI checks the protected
`/api/v2/user/verified-status` endpoint to distinguish rejected credentials from
an unavailable identity response. A successful viewer-token request, WebSocket
connection, or browser session ID alone does not prove account sign-in: the
viewer service also accepts anonymous connections. Use an account Bearer token
from the Authorization header of an authenticated Kick account API request,
rather than the viewer token or `x-client-token`.

HTTP 401 during operation
closes the active connection and returns to the login prompt, then
restarts after successful verification. Automatic token refresh is not
implemented. Network errors, HTTP 403 access restrictions, and HTTP 429 rate
limits do not overwrite saved credentials. Use `login --with-session` to
compare authentication with browser session cookies when needed. Live API
behavior and any account level or point credit are controlled by Kick.

## Configuration

- Windows: `%APPDATA%\kick-level-farm\config.json`
- Linux: `$XDG_CONFIG_HOME/kick-level-farm/config.json`, or
  `~/.config/kick-level-farm/config.json` when the variable is unset.

Settings are independent of the working directory. The token and any optional
session cookies are stored as
**plain text**. On Linux, new config directories use `0700` and files use `0600`.
On Windows, the file inherits access permissions from the user's AppData
directory. Writes use a temporary file and atomic replacement. Do not share
the config file or add it to source control.

## Building from source

Requires Go 1.26.4 or later:

```sh
go run .
go test ./...
go vet ./...
go build -trimpath -o kick-level-farm .
```

Build both platform executables:

```powershell
# Windows / PowerShell
.\scripts\build.ps1
```

```sh
# Linux
sh scripts/build.sh
```

Binaries are written to `dist/`. End users do not need Go installed. Personal
tokens and cookies from the original desktop examples have not been copied.
Network tests use local HTTP/WebSocket servers without accessing a real account.
