# Shipping the iOS app to TestFlight

One entry point: **`ios/ship-testflight.sh`**. `kraken ship-ios` and the board's
ship button both shell out to it; `make ios-testflight` is an alias for humans.
It archives the app, exports a `.ipa`, and uploads that to App Store Connect.

> **The upload leg has never been executed.** Everything up to and including the
> export was built and verified against Xcode 26.3 on this machine, but no build
> has ever actually been sent to App Store Connect — there is no API key and no
> distribution certificate yet. Treat the first real run as a debugging session,
> not a routine deploy.

## Three things only you can set up

None of these can be automated, and the pipeline is useless until all three exist.

### 1. An App Store Connect API key

App Store Connect → **Users and Access** → **Integrations** → **App Store Connect
API** → generate a key with the **App Manager** role.

It yields three things: a **Key ID**, an **Issuer ID**, and a `.p8` file that
**downloads exactly once**. If you lose the `.p8` there is no recovery — revoke
the key and make a new one.

Put the file where `altool` looks for it by convention:

```
~/.appstoreconnect/private_keys/AuthKey_<Key ID>.p8
```

(`altool` searches `./private_keys`, `~/private_keys`, `~/.private_keys`, and
`~/.appstoreconnect/private_keys`. The script never names the file on the
command line, so the key path stays out of the process table and the logs.)

### 2. An app record for `dev.carl.Kraken`

App Store Connect → **Apps** → **+** → **New App**. The bundle id must match
exactly. Uploads for an app that does not exist are rejected.

### 3. An Apple Distribution certificate — created on your first run

**This machine does not have one.** It has only
`Apple Development: Carl Zhang (J43AYT4Q4V)`, which is enough to build and to
archive but *not* to export for the App Store.

Creating one means letting `xcodebuild` talk to the Apple Developer website and
**write to your real developer account** — certificates, provisioning profiles,
and App IDs. Nothing in this repo does that on its own. You opt in, once:

```sh
ios/ship-testflight.sh --allow-provisioning-updates
```

After that first run the assets exist and plain `ios/ship-testflight.sh` works.
The board button never passes this flag.

## Environment

```sh
export ASC_KEY_ID=<Key ID>
export ASC_ISSUER_ID=<Issuer ID>
```

Both come from the same App Store Connect page as the key. They are read from
the environment only — never committed, never echoed into
`~/.kraken/ship-ios.log`, never written into `~/.kraken/*.json`. Preflight fails
with instructions if either is missing or if the `.p8` is not where it belongs.

## Running it

```sh
ios/ship-testflight.sh --dry-run   # preflight + print the plan, execute nothing
ios/ship-testflight.sh             # the real thing
make ios-testflight ARGS=--dry-run # same, via make
```

A run is: preflight → `xcodegen generate` → `xcodebuild archive` →
`xcodebuild -exportArchive` → `xcrun altool --upload-app`. Each phase prints
`KRAKEN-PHASE: <name>` so `kraken ship-ios` can track progress; the resolved
build number prints once as `KRAKEN-BUILD: <n>`. Those two lines are an
interface — keep them stable.

Expect roughly ten minutes, most of it in archive and upload.

## The build number

`CFBundleVersion` is `git rev-list --count HEAD` — the number of commits on
`HEAD` — passed to `xcodebuild` as `CURRENT_PROJECT_VERSION`.

This matters more than it looks. **App Store Connect permanently burns a
(`CFBundleShortVersionString`, `CFBundleVersion`) pair on first upload and will
never accept that pair again.** A duplicate wastes an entire archive+upload
cycle and cannot be undone. The commit count is monotonic, needs no state file,
and gives the same commit the same number on a rebuild — which is correct, since
it is the same build.

Three consequences:

- **Preflight refuses a dirty tree.** Otherwise the `.ipa` would contain
  uncommitted work while claiming to be commit N, and the number is unfixable
  afterwards.
- **Preflight refuses anything but `main`** (`jobs.PreflightShipIOS`, mirroring
  `PreflightUpdate`). A feature branch — or a detached HEAD — has its own commit
  count, and a count *below* main's is one App Store Connect may already own.
  That collision is the unrecoverable one, so it is refused rather than risked.
  It is also why a ship always runs in `~/Developer/kraken` and never in the
  caller's worktree.
- `ExportOptions.plist` sets `manageAppVersionAndBuildNumber` to `false`.
  Xcode's default is `true`, meaning it feels free to rewrite the build number
  during export — which would throw away the deterministic numbering.

Bumping `CFBundleShortVersionString` (the `1.0` in `ios/project.yml`) resets the
namespace, so build numbers may repeat across marketing versions.

## Export compliance

`ITSAppUsesNonExemptEncryption` is `false` in the app's Info.plist. The app uses
no encryption beyond system TLS. Without this key **every** upload parks behind
a manual export-compliance question in App Store Connect and never reaches
testers until a human clicks through, which would defeat shipping from the
board. Verify it survives any Info.plist change:

```sh
plutil -p <built Kraken.app>/Info.plist | grep ITSAppUsesNonExemptEncryption
```

## What "done" means

**Uploaded, not installed.** When the pipeline reports `done`, the build has
reached App Store Connect and nothing more. TestFlight then has to *process* it
— typically a few minutes — before the phone is even offered the update, and
installing it is a separate tap. If you ship from the phone, the app you are
holding is the one being replaced; it will not change under you at `done`.

## What the first run looks like

Without the distribution certificate, the archive **succeeds** — it signs with
the development identity — and the export is what stops:

```
error: exportArchive No profiles for 'dev.carl.Kraken' were found
** EXPORT FAILED **
```

That is the expected first-run outcome, not a bug. A green archive does **not**
mean signing is set up; only the export proves it. Re-run once with
`--allow-provisioning-updates` (see prerequisite 3) and it will create what it
needs.

## Files

| File | Role |
|---|---|
| `ios/ship-testflight.sh` | the pipeline; the only entry point |
| `ios/ExportOptions.plist` | `-exportArchive` options (`method: app-store-connect`) |
| `ios/project.yml` | `CURRENT_PROJECT_VERSION`, `ITSAppUsesNonExemptEncryption` |

Tool spellings were checked against Xcode 26.3 (`altool` 26.10.1) rather than
assumed: `method: app-store-connect` is current and `app-store` is the
deprecated alias, and `altool --upload-app` is still the App Store upload path
(`notarytool` is for Developer ID notarization and cannot do this). Re-check
with `xcodebuild -help` and `xcrun altool --help` after an Xcode upgrade.
