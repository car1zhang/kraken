# Shipping the iOS app to TestFlight

One entry point: **`ios/ship-testflight.sh`**. `kraken ship-ios` and the board's
ship button both shell out to it; `make ios-testflight` is an alias for humans.
It archives the app, exports a `.ipa`, and uploads that to App Store Connect.

> **Verified end to end on 2026-07-26.** Build **218** went preflight → archive →
> export → upload and App Store Connect accepted it (`UPLOAD SUCCEEDED`, delivery
> UUID `5d5f32dc-551f-4762-b713-6b1fb33452fb`), including the one-time
> `--allow-provisioning-updates` run that created the App Store signing assets.
> The whole run took about six minutes. What is still unproven is everything
> *after* the upload: TestFlight processing, tester distribution, and install on
> a device.

## Four things only you can set up

None of these can be automated, and the pipeline is useless until all four exist.
The fourth is the easiest to forget and the hardest to diagnose: without it an
upload succeeds, processes, and then sits in App Store Connect where no phone
will ever be offered it — which looks exactly like a broken pipeline.

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

### 3. App Store signing assets — created on your first run

The keychain has only `Apple Development: Carl Zhang (J43AYT4Q4V)`, which is
enough to build and archive but *not* to export for the App Store.

Fixing that means letting `xcodebuild` talk to the Apple Developer website and
**write to your real developer account** — provisioning profiles, App IDs, and
signing certificates. Nothing in this repo does that on its own. You opt in, once:

```sh
ios/ship-testflight.sh --allow-provisioning-updates
```

After that first run the assets exist and plain `ios/ship-testflight.sh` works.
The board button never passes this flag.

**What it actually created here** (measured on the first real run, 2026-07-26):
a new `iOS Team Store Provisioning Profile: dev.carl.Kraken` — the App Store
profile, distinguishable from the development one by `get-task-allow: false`.
Notably it did **not** add an `Apple Distribution` identity to the keychain;
`security find-identity -v -p codesigning` still lists only the development
cert. Xcode used a **cloud-managed distribution certificate**, whose private key
stays on Apple's servers. So "no distribution cert in the keychain" is not
evidence that signing is unset up — check for the Store profile instead.

### 4. An internal testing group with you in it

App Store Connect → your app → **TestFlight** → **Internal Testing** → **+** →
create a group → add your Apple ID → enable **Automatically distribute builds**
(otherwise each build still needs a manual "add to group" click).

Internal testers need no review: a build is installable as soon as processing
finishes. Skip this and the pipeline still reports a clean `done` — the build
just never reaches a device, which is the most confusing possible failure mode
because nothing anywhere reports an error.

Also worth doing once, on the phone: TestFlight → the app → enable **automatic
updates**, so a shipped build installs itself instead of waiting for a tap.
That is the closest this gets to a true remote reinstall.

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
