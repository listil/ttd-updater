# ttd-updater

A small Windows utility that checks a Google Drive folder for the latest release of
[TTD (Torchlight Tracker Diablo)](https://ttdiablo.com) — a fan-made companion tool for
*Torchlight: Infinite* — and downloads/applies it if a newer version is available.

This tool does not build or distribute TTD itself. TTD's releases are published and maintained
separately by its own team; this utility only checks that existing public Google Drive folder and
automates the "download the new zip and copy it over my install" step.

## What it does

1. Checks the local install for its current version (`version_info.json`, or a versioned filename
   if present).
2. Scrapes the public Google Drive folder's page for the newest `TTD*.zip` and compares versions.
3. If newer, downloads just that one file (not the whole Drive folder), extracts it, and syncs it
   onto the install directory — the main app executable is always written under a fixed name
   (`TTD.exe`) regardless of the versioned filename inside the zip, so a desktop shortcut to it
   never breaks across updates.
4. Shows a small window with the result and optional "launch TTD / launch the game now" checkboxes,
   plus an option to (re)create a desktop shortcut.

## Building

Requires Go and the [Wails v2](https://wails.io) CLI:

```
go install github.com/wailsapp/wails/v2/cmd/wails@latest
wails build -clean
```

The compiled binary is written to `build/bin/ttd_updater.exe`. Building with plain `go build`
instead of the Wails CLI will produce a broken exe — see `CLAUDE.md` for why.

## Stack

Go backend, no cgo. Frontend is plain HTML/CSS/JS (no bundler) rendered in a WebView2 window —
no bundled browser engine, just the one already on Windows 10/11.
