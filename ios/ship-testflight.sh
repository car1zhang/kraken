#!/bin/bash
#
# Build the iOS app and upload it to TestFlight.
#
# THE single entry point for shipping the phone app. `kraken ship-ios` shells
# out to this script rather than reimplementing the sequence in Go, exactly as
# `kraken update` shells out to `make install` — the shape of the work is
# "drive four Apple CLIs in order", which is a shell's job, and one
# implementation means the board button and a terminal run can never drift.
# `make ios-testflight` is a thin alias for humans; it adds nothing.
#
# Setup, prerequisites, and what the first run does to carl's Apple developer
# account: ios/TESTFLIGHT.md. Read that before the first run.
#
# Usage:
#   ios/ship-testflight.sh              build, export, upload
#   ios/ship-testflight.sh --dry-run    run preflight, print the plan, do nothing
#   ios/ship-testflight.sh --allow-provisioning-updates
#                                       ALSO let Xcode create signing assets
#                                       (certificates, profiles, App IDs) on
#                                       carl's real Apple developer account.
#                                       Needed exactly once, for the first run.
#                                       Off by default — see ios/TESTFLIGHT.md.
#
# Environment (see ios/TESTFLIGHT.md for where to get these):
#   ASC_KEY_ID       App Store Connect API key id
#   ASC_ISSUER_ID    App Store Connect issuer id
#   the matching private key at ~/.appstoreconnect/private_keys/AuthKey_$ASC_KEY_ID.p8
#
# Machine-readable output: every phase transition prints one line
#   KRAKEN-PHASE: <preflight|generating|archiving|exporting|uploading|done>
# and the resolved build number prints once as
#   KRAKEN-BUILD: <n>
# so the Go verb can track progress by scanning stdout instead of guessing from
# elapsed time. Keep these stable; they are an interface.

set -euo pipefail

# Resolve the repo root from the script's own location. The Go verb may invoke
# this with any cwd, and every path below is repo-relative.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

SCHEME="Kraken"
PROJECT="ios/Kraken.xcodeproj"
EXPORT_OPTIONS="ios/ExportOptions.plist"

DRY_RUN=0
# Empty by default, and that default is load-bearing: -allowProvisioningUpdates
# lets xcodebuild talk to the Apple Developer website and CREATE certificates,
# provisioning profiles, and App IDs on the real account. Automation must never
# do that silently, so it is opt-in per run rather than baked into the commands
# below. The board button does not pass it; carl's first run does.
#
# Expanded below as ${PROV_FLAG[@]+"${PROV_FLAG[@]}"}, never as the obvious
# "${PROV_FLAG[@]}": macOS ships bash 3.2, where expanding an EMPTY array under
# `set -u` is an "unbound variable" fatal. The plain form aborts every run that
# does not pass this flag — i.e. all of them after the first.
PROV_FLAG=()
PROV_LABEL="none — will not create or modify signing assets"
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --allow-provisioning-updates)
      PROV_FLAG=(-allowProvisioningUpdates)
      PROV_LABEL="-allowProvisioningUpdates — MAY CREATE certs/profiles/App IDs on the real Apple account"
      ;;
    # The header comment IS the help text — one copy, so they cannot drift.
    # The range is STRUCTURAL (print from line 2 until the first line that is
    # neither a comment nor blank, then quit) rather than the line numbers this
    # used to hardcode: `2,35p` silently truncated the help the moment anyone
    # added a line to the header, and nothing anywhere would have said so.
    -h|--help) sed -n '2,${/^#/!q;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "ship-testflight: unknown flag: $arg" >&2; exit 2 ;;
  esac
done

phase() { echo "KRAKEN-PHASE: $1"; }
die()   { echo "ship-testflight: $*" >&2; exit 1; }

# ---------------------------------------------------------------- preflight

phase preflight

command -v xcodegen  >/dev/null 2>&1 || die "xcodegen not found — brew install xcodegen"
command -v xcodebuild >/dev/null 2>&1 || die "xcodebuild not found — install Xcode and run xcode-select --switch"
command -v git       >/dev/null 2>&1 || die "git not found"

# A dirty tree means the build number (derived from the commit count) would not
# describe what actually shipped: the .ipa would contain uncommitted work while
# claiming to be commit N. Since App Store Connect burns that build number
# forever, there is no way to correct the record afterwards. Refuse instead.
if [ -n "$(git status --porcelain)" ]; then
  die "working tree is dirty — commit or stash first, so the build number matches what ships"
fi

# Credentials come from the environment and the key file NEVER enters the repo.
# Neither value is echoed anywhere below: the key id is half of a credential
# pair and this script's output is written to ~/.kraken/ship-ios.log.
#
# Fall back to ~/.kraken/asc.env for anything the environment does not carry.
# kraken ship-ios already injects these (jobs.ASCEnviron), so this branch is
# for a hand-run in a shell that has not sourced them — and it is deliberately
# the same file, so the button and the terminal can never disagree about which
# key is in use. Read with grep+cut rather than sourcing it: sourcing would
# execute whatever is in the file, and a credential store should not also be a
# code path.
for _k in ASC_KEY_ID ASC_ISSUER_ID; do
  if [ -z "$(eval "printf '%s' \"\${$_k:-}\"")" ] && [ -r "$HOME/.kraken/asc.env" ]; then
    _v="$(grep -E "^[[:space:]]*(export[[:space:]]+)?$_k=" "$HOME/.kraken/asc.env" 2>/dev/null |
          tail -1 | cut -d= -f2- | tr -d '"'\''' | xargs)"
    [ -n "$_v" ] && eval "export $_k=\"\$_v\""
  fi
done
unset _k _v

if [ -z "${ASC_KEY_ID:-}" ] || [ -z "${ASC_ISSUER_ID:-}" ]; then
  die "App Store Connect credentials missing.

  Set both:
    export ASC_KEY_ID=<Key ID>
    export ASC_ISSUER_ID=<Issuer ID>

  Both are shown at App Store Connect → Users and Access → Integrations →
  App Store Connect API. Generate a key with the App Manager role; the .p8
  downloads exactly once and belongs at
    ~/.appstoreconnect/private_keys/AuthKey_<Key ID>.p8
  (that directory is one of the four altool searches by convention).

  Full setup: ios/TESTFLIGHT.md"
fi

# Check the key file early. altool would report this too, but only after the
# archive and export have already burned several minutes.
#
# -s and -r, not just -f: a zero-byte file (an interrupted download, a `touch`
# standing in for the real key) and a mode-000 one (a copy that lost its
# permissions) both pass -f and then fail at altool — which is precisely the
# several minutes this check exists to save.
P8="$HOME/.appstoreconnect/private_keys/AuthKey_${ASC_KEY_ID}.p8"
if [ ! -s "$P8" ] || [ ! -r "$P8" ]; then
  die "no readable App Store Connect private key at ~/.appstoreconnect/private_keys/AuthKey_\$ASC_KEY_ID.p8
  (missing, empty, or not readable by this user)

  The .p8 is downloadable exactly once, when the key is created. If it is lost,
  revoke that key in App Store Connect and generate a new one.

  Full setup: ios/TESTFLIGHT.md"
fi

# Build number: the number of commits on HEAD. Monotonic (commits only
# accumulate), deterministic (no state file to drift or be lost with the
# worktree), and rebuilding the same commit yields the same number — which is
# correct, because it is the same build. Deliberately NOT a timestamp (two
# builds of one commit would differ) and NOT a counter file (dies with the
# checkout, and silently resets to a number App Store Connect already owns).
BUILD_NUMBER="$(git rev-list --count HEAD)"
echo "KRAKEN-BUILD: $BUILD_NUMBER"

# Everything intermediate lives outside the repo. An .xcarchive is hundreds of
# MB and there is no reason for it to be anywhere git might see it.
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kraken-testflight.XXXXXX")"
ARCHIVE_PATH="$WORK_DIR/Kraken.xcarchive"
EXPORT_DIR="$WORK_DIR/export"

# ios/Kraken.xcodeproj/project.pbxproj is TRACKED, and the generate phase below
# rewrites it in place. Today xcodegen reproduces it byte for byte, so nothing
# shows up in `git status` — but that is luck, not a guarantee: a `brew upgrade
# xcodegen` that changes one line of formatting, or a project.yml edit committed
# without regenerating, makes the first ship dirty the tree. And a dirty tree
# here is not a nuisance, it is two dead buttons: this script's own clean-tree
# gate refuses every later ship, and PreflightUpdate's dirty check refuses every
# later update, neither with a remedy anyone could reach from the board.
#
# So the file is snapshotted before xcodegen touches it and put back on the way
# out, whatever the exit path. Restoring rather than generating somewhere else
# on purpose: an .xcodeproj records its source paths relative to its own
# location, so moving it out of ios/ would rewrite every one of them.
PBXPROJ="$PROJECT/project.pbxproj"
PBXPROJ_BACKUP=""

cleanup() {
  if [ -n "$PBXPROJ_BACKUP" ] && [ -f "$PBXPROJ_BACKUP" ] && ! cmp -s "$PBXPROJ_BACKUP" "$PBXPROJ"; then
    cp "$PBXPROJ_BACKUP" "$PBXPROJ"
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

if [ "$DRY_RUN" = "1" ]; then
  cat <<EOF

DRY RUN — preflight passed, nothing below was executed.

  scheme          $SCHEME
  project         $PROJECT
  build number    $BUILD_NUMBER   (git rev-list --count HEAD)
  archive         $ARCHIVE_PATH
  export options  $EXPORT_OPTIONS
  credentials     ASC_KEY_ID and ASC_ISSUER_ID set; key file present
  provisioning    $PROV_LABEL

Would run:
  1. xcodegen generate --spec ios/project.yml --project ios
  2. xcodebuild archive -project $PROJECT -scheme $SCHEME \\
       -destination 'generic/platform=iOS' -configuration Release \\
       -archivePath $ARCHIVE_PATH CURRENT_PROJECT_VERSION=$BUILD_NUMBER
  3. xcodebuild -exportArchive -archivePath $ARCHIVE_PATH \\
       -exportPath $EXPORT_DIR -exportOptionsPlist $EXPORT_OPTIONS
  4. xcrun altool --upload-app -f $EXPORT_DIR/Kraken.ipa -t ios \\
       --apiKey <\$ASC_KEY_ID> --apiIssuer <\$ASC_ISSUER_ID>

EOF
  phase done
  exit 0
fi

# ------------------------------------------------------------------ generate

phase generating
# Regenerate the pbxproj from project.yml so a stale checked-in project can
# never ship a build that disagrees with the spec (the build number plumbing
# lives in project.yml, so this matters more than usual here). The snapshot is
# taken first and restored by the EXIT trap — see cleanup() for why leaving the
# tracked file rewritten would wedge both buttons.
if [ -f "$PBXPROJ" ]; then
  PBXPROJ_BACKUP="$WORK_DIR/project.pbxproj.orig"
  cp "$PBXPROJ" "$PBXPROJ_BACKUP"
fi
xcodegen generate --spec ios/project.yml --project ios

# ------------------------------------------------------------------- archive

phase archiving
# generic/platform=iOS, not a concrete device: an App Store archive must be a
# device build for all arm64 iOS hardware, not one thinned for whatever phone
# happens to be plugged in.
#
# Measured: this step SUCCEEDS on a machine with no distribution certificate —
# it signs with the existing "Apple Development" identity and the team
# provisioning profile. The App Store signing requirement is not enforced here
# but at export (below), so do not read a green archive as "signing is set up".
xcodebuild archive \
  -project "$PROJECT" \
  -scheme "$SCHEME" \
  -destination 'generic/platform=iOS' \
  -configuration Release \
  -archivePath "$ARCHIVE_PATH" \
  CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
  ${PROV_FLAG[@]+"${PROV_FLAG[@]}"}

# -------------------------------------------------------------------- export

phase exporting
# THIS is where a machine without App Store signing assets stops. Measured on a
# machine with only an "Apple Development" identity, the exact failure is:
#
#   error: exportArchive No profiles for 'dev.carl.Kraken' were found
#   ** EXPORT FAILED **
#
# That is the expected FIRST-RUN outcome, not a bug. Re-run once with
# --allow-provisioning-updates to let Xcode create the Apple Distribution
# certificate and App Store provisioning profile; afterwards plain runs work.
xcodebuild -exportArchive \
  -archivePath "$ARCHIVE_PATH" \
  -exportPath "$EXPORT_DIR" \
  -exportOptionsPlist "$EXPORT_OPTIONS" \
  ${PROV_FLAG[@]+"${PROV_FLAG[@]}"}

IPA="$(/usr/bin/find "$EXPORT_DIR" -maxdepth 1 -name '*.ipa' -print -quit)"
[ -n "$IPA" ] || die "export produced no .ipa in $EXPORT_DIR"

# -------------------------------------------------------------------- upload

phase uploading
# altool --upload-app is still the current App Store upload path under Xcode
# 26.3 (altool 26.10.1); notarytool is for Developer ID notarization and cannot
# do this. --apiKey/--apiIssuer and --api-key/--api-issuer are both accepted;
# the key file itself is never named on the command line — altool finds
# AuthKey_<key id>.p8 by convention, which keeps the credential path out of
# both the process table and this script's log.
xcrun altool --upload-app \
  -f "$IPA" \
  -t ios \
  --apiKey "$ASC_KEY_ID" \
  --apiIssuer "$ASC_ISSUER_ID"

phase done
echo
echo "uploaded build $BUILD_NUMBER to App Store Connect."
echo "TestFlight still has to PROCESS it (usually a few minutes) before the"
echo "phone is offered the update — 'done' here means uploaded, not installed."
