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
`~/.appstoreconnect/private_keys`, so the script never has to name the file when
it uploads. `xcodebuild` has no such convention and must be given the path — see
the `No Accounts` corollary in prerequisite 3 for why it is given it on exactly
one step, and what that does and does not expose.)

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

**The corollary, learned the hard way on 2026-07-27.** A cloud-managed
certificate is not installed once and kept; it is issued to the export *on every
run*, and that request has to be authenticated. So App Store signing here depends
on a live credential, not on a file sitting in the keychain — and when that
credential goes away, eight consecutive green ships turn into:

```
error: exportArchive No Accounts
error: exportArchive No signing certificate "iOS Distribution" found
```

The second line is the misleading one: nothing was deleted, and re-running with
`--allow-provisioning-updates` does not help, because the problem is that
`xcodebuild` has nobody to authenticate *as*. By default it authenticates as
whatever Apple ID is signed into Xcode's Accounts pane — which is a terrible
dependency for this pipeline, since a board-triggered ship is a launchd job with
no login session and no way to answer a sign-in prompt.

The script therefore passes the App Store Connect API key to `xcodebuild` itself
(`-authenticationKeyPath` / `-authenticationKeyID` / `-authenticationKeyIssuerID`),
not just to `altool`. The same `~/.kraken/asc.env` credentials that upload the
build now also authorize the signing, so the pipeline no longer depends on an
Xcode account existing at all. If you ever see `No Accounts`, the key — not the
keychain — is what to check.

It goes on the **export step only**, and that placement is load-bearing rather
than tidy. `-authenticationKeyPath` names a file whose name embeds the key id, so
those flags are the one place this pipeline puts the key PATH into an argv. (The
two ids are on altool's own command line at the upload, and always have been.) The
archive step does not need them — it signs with the local development identity —
and it is the one `xcodebuild` action that echoes its own full command line as its
first line of output, which `shipScanner` tees verbatim into
`~/.kraken/ship-ios.log`, a world-readable append-only file that is never rotated.
Measured across that log's twelve recorded invocations: twelve archive echoes,
zero export echoes. Keeping the flags on the export alone therefore satisfies
`No Accounts` while leaving the key id out of every log — verified 2026-07-27 by
running an archive with no credentials and an export with them, which succeeded.

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

## Credentials: `~/.kraken/asc.env`

```sh
umask 077 && cat > ~/.kraken/asc.env <<'EOF'
ASC_KEY_ID=<Key ID>
ASC_ISSUER_ID=<Issuer ID>
EOF
```

Both the board button and hand-runs read this file, so the two can never
disagree about which key is in use. An explicit `export` in your shell
overrides it, so a new key can be tested without editing anything.

**Do not try to use `launchctl setenv` instead.** It looks like the right
answer and it does not work — measured here: the variables land in the `gui/`
domain (`launchctl getenv` returns them) and never reach the job, because the
plist declares its own `EnvironmentVariables` dict. Neither `kickstart -k` nor
a full `bootout`+`bootstrap` changes that, and `setenv` is lost on reboot
anyway. The failure is nasty because it is asymmetric: terminal runs keep
working while the button refuses preflight forever.

Only `ASC_KEY_ID` and `ASC_ISSUER_ID` are read from the file, and it is parsed
rather than sourced — a credential store should not also be a code path.

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
