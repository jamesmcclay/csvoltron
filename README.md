# csvoltron 🤖📄

*Defender of spreadsheets. Mortal enemy of configuration cruft.*

Strata Cloud Manager's **Config Cleanup / Optimize** page (Unused Objects,
Zero Hit Objects, Zero Hit Policy Rules) has no "export to CSV" button, and 
isn't part of any public API. So `csvoltron` opens a real Chrome window, 
lets you log in like a human (SSO, MFA, the works), quietly notices the 
access token your browser is already using, and then politely asks the API 
for the data itself. No clicking through tabs, no copy-pasting tables, 
no dignity lost.

It writes out 3 timestamped CSVs you can drop straight into Excel, Sheets,
or your weekly "please clean up your firewall rules" email.

## Quick start (Windows)

You don't need Go installed. You don't even need `git`. Open **PowerShell**
and paste this:

```powershell
$root = Join-Path $env:USERPROFILE "csvoltron-run"
mkdir $root -Force | Out-Null
Set-Location $root

if (!(Test-Path .\go\bin\go.exe)) {
  $v = (Invoke-WebRequest "https://go.dev/VERSION?m=text" -UseBasicParsing).Content.Split("`n")[0].Trim()
  curl.exe -L "https://go.dev/dl/$v.windows-amd64.zip" -o go.zip
  Expand-Archive go.zip . -Force
}

Remove-Item csvoltron-main -Recurse -Force -ErrorAction Ignore
Invoke-WebRequest "https://github.com/jamesmcclay/csvoltron/archive/refs/heads/main.zip" -OutFile repo.zip
Expand-Archive repo.zip . -Force
Set-Location csvoltron-main

..\go\bin\go.exe run .

```

What that does, in order: grabs a portable copy of Go (cached for next time,
~150MB the first run only), downloads this repo as a zip (no `git` needed),
and runs the tool. A Chrome window will pop up — log in, do your MFA dance,
and once you land on the Optimize page, `csvoltron` notices and takes it
from there automatically. No need to press anything.

Run the same block again any time you want a fresh export — it'll skip the
Go download (already cached) and re-fetch just the repo and your data.

**Requirements:** Windows 10/11, PowerShell, Google Chrome installed, and
network access to both `go.dev`/`github.com` and your Strata Cloud Manager
tenant.

### Locked-down corporate Chrome

If `csvoltron` fails with `chrome failed to start: ... DevTools remote
debugging is disallowed by the system admin`, your company's Chrome policy
blocks the automation csvoltron needs. Use this installer instead -- it also
downloads a portable, unmanaged Chromium that isn't subject to that policy:

```powershell
$root = Join-Path $env:USERPROFILE "csvoltron-run"
mkdir $root -Force | Out-Null
Set-Location $root

if (!(Test-Path .\go\bin\go.exe)) {
  $v = (Invoke-WebRequest "https://go.dev/VERSION?m=text" -UseBasicParsing).Content.Split("`n")[0].Trim()
  curl.exe -L "https://go.dev/dl/$v.windows-amd64.zip" -o go.zip
  Expand-Archive go.zip . -Force
}

if (!(Test-Path .\chromium\chrome-win\chrome.exe)) {
  $rev = (Invoke-WebRequest "https://commondatastorage.googleapis.com/chromium-browser-snapshots/Win_x64/LAST_CHANGE" -UseBasicParsing).Content.Trim()
  curl.exe -L "https://commondatastorage.googleapis.com/chromium-browser-snapshots/Win_x64/$rev/chrome-win.zip" -o chromium.zip
  Expand-Archive chromium.zip chromium -Force
}

Remove-Item csvoltron-main -Recurse -Force -ErrorAction Ignore
Invoke-WebRequest "https://github.com/jamesmcclay/csvoltron/archive/refs/heads/main.zip" -OutFile repo.zip
Expand-Archive repo.zip . -Force
Set-Location csvoltron-main

..\go\bin\go.exe run . -portable

```

Run the same block again any time you want a fresh export -- both the Go and
Chromium downloads are cached after the first run. Once set up, you can also
double-click `run-portable.bat` in `csvoltron-main` instead of re-pasting
this block.

## Quick start (macOS / Linux / "I already have Go")

```sh
git clone https://github.com/jamesmcclay/csvoltron.git
cd csvoltron
go run .
```

### Locked-down corporate Chrome (macOS)

If `csvoltron` fails with `... DevTools remote debugging is disallowed by
the system admin`, or just hangs and times out waiting for a websocket URL,
your company's Chrome policy blocks the automation csvoltron needs. Use
this instead -- it downloads a portable, unmanaged Chromium as a sibling of
your checkout and runs with `-portable`:

```sh
mkdir -p ~/csvoltron-run && cd ~/csvoltron-run

if [ ! -x chromium/chrome-mac/Chromium.app/Contents/MacOS/Chromium ]; then
  bucket=Mac; [ "$(uname -m)" = "arm64" ] && bucket=Mac_Arm
  rev=$(curl -fsSL "https://commondatastorage.googleapis.com/chromium-browser-snapshots/$bucket/LAST_CHANGE")
  curl -fsSL "https://commondatastorage.googleapis.com/chromium-browser-snapshots/$bucket/$rev/chrome-mac.zip" -o chromium.zip
  mkdir -p chromium && unzip -q -o chromium.zip -d chromium && rm chromium.zip
  xattr -dr com.apple.quarantine chromium/chrome-mac/Chromium.app
fi

rm -rf csvoltron
git clone https://github.com/jamesmcclay/csvoltron.git
cd csvoltron

go run . -portable
```

Run the same block again any time you want a fresh export -- the Chromium
download is cached after the first run. (Linux isn't supported by
`-portable` yet.)

## What you get

After a run, look in `csv_output/` for:

| File | What's in it |
|---|---|
| `unused_objects_<timestamp>.csv` | Objects nobody references anymore |
| `zero_hit_objects_<timestamp>.csv` | Security rules with zero-hit objects inside them (source/dest/app/etc.) |
| `zero_hit_policy_rules_<timestamp>.csv` | Whole rules that have never matched traffic |

Every run gets its own timestamp down to the second, so feel free to run it
as often as you like without clobbering yesterday's export.

## Useful flags

```sh
go run . -out-dir ./csv_output -login-timeout 5m
```

| Flag | Default | What it does |
|---|---|---|
| `-out-dir` | `./csv_output` | Where the CSVs go |
| `-start-url` | the Optimize page | Where the browser opens first |
| `-profile-dir` | `./.csvoltron-chrome-profile` | Persistent Chrome profile, so you don't have to redo MFA every single run |
| `-login-timeout` | `5m` | How long to wait for you to finish logging in before giving up |
| `-portable` | `false` | Use the portable Chromium downloaded by the installer instead of system Chrome -- see "Locked-down corporate Chrome" under your OS's quick start above |

## How it actually works (the fun part)

Strata Cloud Manager authenticates its own API calls with a short-lived
bearer token (`x-auth-jwt`, ~15 minutes), not a magic cookie. So instead of
trying to fully script SSO + MFA headlessly (good luck), `csvoltron`:

1. Opens a real, visible Chrome window and lets *you* be the human in the
   loop for login + MFA — the part that genuinely needs a human.
2. Watches the network traffic just long enough to spot that token and the
   tenant's API host.
3. Closes the browser and switches to plain old `net/http` for the actual
   data pulls — fast, scriptable, and not dependent on a browser staying
   open.

If you're curious what the raw API traffic looks like, or PAN ever changes
these endpoints, `cmd/discover` is the diagnostic sibling tool that dumps
full request/response traffic to JSON for poking around:

```sh
go run ./cmd/discover
```

## Disclaimer

This talks to an internal, unofficial/undocumented API that the SCM web UI itself
uses — not a published/supported one. If Palo Alto Networks changes it,
`csvoltron` may need a tune-up. PRs welcome. 🛠️

## License

Licensed under either of [Apache License, Version 2.0](LICENSE-APACHE) or
[MIT license](LICENSE-MIT) at your option.
