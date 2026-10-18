# AutoTTV

AutoTTV is a small Go desktop utility that watches a prioritized list of Twitch channels and keeps a browser window open on whichever one is live. When nothing you care about is live, the window is closed.

Everything is configured from a local web dashboard at `http://localhost:3000`.

The dashboard is also reachable from other devices on your LAN at `http://<host-ip>:3000` for checking status and managing channels. Twitch credential setup and logout only work from the host machine itself, because Twitch requires the OAuth redirect URI to be `localhost`. There is no LAN authentication — only run AutoTTV on a trusted network.

## What it does

- **Watches up to 5 channels in priority order.** Twitch notifies AutoTTV the moment a stream goes online or offline (EventSub over WebSockets, no polling). The first channel in your list that is live is the one that gets opened. If a higher-priority channel comes online, the current window is closed and the new one opened; if the open channel ends, the next live one takes over.
- **Opens a dedicated browser window.** Streams launch in an isolated Chrome/Edge/Firefox window with its own profile, so it never interferes with your normal browsing. On Windows the window is pushed to the foreground so the player actually renders on remote-desktop and headless setups.
- **Optionally watches drop campaigns for a game.** Pick a game category and AutoTTV lists its active Twitch drop campaigns (scraped from twitchdrops.app). "Auto Watch" opens a random participating stream and tracks how long you've watched. Monitored channels take priority: if one of them goes live, the drops stream is replaced.
- **Keeps your credentials local.** Your Twitch Client ID, Client Secret and refresh token are stored in a `0600` config file under your OS user config directory (`~/.config/autoTtv/credentials.json` on Linux, `%AppData%\autoTtv\credentials.json` on Windows). Nothing is sent anywhere except Twitch.

### Why only 5 channels?

Twitch EventSub caps each user token at a total subscription "cost" of 10. Watching a channel you don't own needs two subscriptions (`stream.online` and `stream.offline`) at a cost of 1 each, so 5 channels is the hard limit the API allows.

## Browser setup

AutoTTV pairs well with extension [BetterTTV](https://chromewebstore.google.com/detail/betterttv/ajopnjidmegmdimjlfnijceegpefgped). Once `BetterTTV` extension is installed, you can configure it to auto claim channel points and drops.  
Next it is a good idea to set the browser to not limit any audio. Occasionally in testing twitch would auto open, but it would be muted. This was resolved with changing some settings in Chrome. Those settings are:
1. Settings
2. Privacy and Security
3. Site Settings
4. Click on the `View permissions and data stored across sites` option
5. Find `Twitch.tv` and expand the options
6. In the list find `www.twitch.tv`
    * make sure *not* to click on `auth.twitch.tv` or `dashboard.twitch.tv`
7. Find the `Sound` option in the list
8. Change to `allow`  

Note: Because `AutoTTV` spawns it's own browser, to keep track of the launched window's PID, you must confiure these settings from the dashboard after `Setup Browser Login` was selected at the bottom of the page. The app will launch a browser window. Here you can login to twitch and configure the extension and settings.

## Setup

### 1. Register a Twitch application

AutoTTV talks to Twitch with your own API credentials, so you need a (free) developer app:

1. Open the [Twitch Developer Console](https://dev.twitch.tv/console) and click **Register Your Application**.
2. Use any name, set **OAuth Redirect URLs** to `http://localhost:3000/callback`, and pick any category.
3. Copy the **Client ID**, then click **New Secret** and copy the **Client Secret**.

If you run AutoTTV on a different port (`-port 8080`), register `http://localhost:8080/callback` instead.

### 2. Build and run

Requires Go 1.25 or newer.x

If `make` command is avaliable:
* Open terminal and run `make`
    * by default it will build for local environment
        * Apple Silicon has not been tested

To build using `go build`:
```bash
go build -trimpath -ldflags="-s -w" -o autottv ./cmd/autottv   # add .exe on Windows
```
To run:
```bash
./autottv            # dashboard on http://localhost:3000
./autottv -port 8080 # custom port
```

The dashboard opens in your default browser automatically.

### 3. Connect to Twitch

Paste the Client ID and Secret into the dashboard and click **Connect with Twitch**. You'll be bounced through Twitch's authorization page and back to the dashboard. The refresh token is saved, so this only happens once.

### 4. Log the stream window into Twitch

The stream window uses its own browser profile, so Twitch doesn't know who you are until you log in there once. Click **Setup Browser Login** on the dashboard, sign in to Twitch in the window that appears, and close it. The profile remembers the login from then on, which is what makes drops and channel points count.

### 5. Add channels

In **Monitored Channels**, add up to 5 channel names, reorder them with the arrows (top = highest priority), and click **Save**. If any of them is already live, a window opens immediately. The list is remembered across restarts.

## Dashboard overview

| Section | Purpose |
|---|---|
| Twitch API Connection | Connect, or **Logout / Clear Keys** to wipe credentials and channels from disk. |
| Current Monitoring Status | Live/offline state for each channel and which one is currently open. |
| Monitored Channels | Ordered list editor (add, remove, move up/down, save). |
| Game Drops Watcher | Set a game, toggle campaign monitoring, start/stop Auto Watch on a campaign. |
| Developer & Testing Controls | Manually open/close a stream window; browser login setup. |

`GET /api/status` returns the same information as JSON if you want to script against it.

## Project layout

```
cmd/autottv/       entry point and main event loop
internal/auth/     credential + settings file, token refresh
internal/twitch/   Helix API client and EventSub WebSocket manager
internal/monitor/  priority-list reconciliation logic (pure, unit tested)
internal/browser/  browser process spawning and foreground handling
internal/drops/    twitchdrops.app scraper
internal/web/      dashboard HTTP server and embedded HTML
```

Run the tests with `go test ./...`.

## Troubleshooting

- **No window opens**: AutoTTV looks for Chrome, Edge or Firefox in their default install locations. Install one of them, or check the console output for the exact error.
- **Window opens but you aren't counted as a viewer**: redo step 4 so the isolated profile is logged in.
- **"Unknown channel(s)" when saving**: channel names are Twitch login names (the part after `twitch.tv/`), not display names.

## License

MIT
