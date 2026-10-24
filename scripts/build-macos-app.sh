#!/bin/bash
# Build a traceable macOS arm64 Saki.app candidate from a clean checkout.
#
# Usage:
#   scripts/build-macos-app.sh /absolute/output-dir
#
# The output directory must be outside this checkout. It receives:
#   Saki.app/                 GUI + Saki-runtime helper, Info.plist, ad-hoc signature
#   Saki.app.zip              ditto archive preserving modes/signature
#   manifest.json             source/toolchain/runner/file hashes/signing facts
#   SHA256SUMS                completed app binaries, plist, zip and manifest
#   codesign.log, validation.log, helper-smoke.log
# The caller may also tee this script's output to <output-dir>/build.log.
#
# This is a packaging helper only. It does not change product source, call a
# provider, or include user store/session/.env data. Requires macOS arm64 with
# bash, Go (from desktop/go.mod), Rust/cargo, Python 3, Xcode/codesign, ditto,
# lipo and plutil.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 /absolute/output-dir (outside checkout)" >&2
  exit 2
fi

OUT_INPUT=$1
case "$OUT_INPUT" in
  /*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac

ROOT=$(cd "$(git rev-parse --show-toplevel)" && pwd -P)
case "$OUT_INPUT" in
  "$ROOT"|"$ROOT"/*) echo "output directory must be outside checkout: $ROOT" >&2; exit 2 ;;
esac
mkdir -p "$OUT_INPUT"
OUT=$(cd "$OUT_INPUT" && pwd -P)
case "$OUT" in
  "$ROOT"|"$ROOT"/*) echo "output directory must be outside checkout: $ROOT" >&2; exit 2 ;;
esac
cd "$ROOT"

if [ -n "$(git status --porcelain)" ]; then
  echo "source checkout is not clean; commit/push before building a candidate" >&2
  git status --short >&2
  exit 3
fi

SOURCE_SHA=$(git rev-parse HEAD)
CI_EVENT_SHA=${GITHUB_SHA:-$SOURCE_SHA}
REF=${GITHUB_REF:-$(git symbolic-ref -q HEAD || echo detached)}
RUN_URL=${RUN_URL:-}
APP_NAME=Saki
APP="$OUT/$APP_NAME.app"
ZIP="$OUT/$APP_NAME.app.zip"
MANIFEST="$OUT/manifest.json"
SUMS="$OUT/SHA256SUMS"
CODESIGN_LOG="$OUT/codesign.log"
VALIDATION_LOG="$OUT/validation.log"
SMOKE_LOG="$OUT/helper-smoke.log"

APP_VERSION=$(sed -n 's/^version = "\(.*\)"/\1/p' Cargo.toml | head -n 1)
case "$APP_VERSION" in
  ''|*[!0-9.]*) echo "could not parse Cargo.toml version" >&2; exit 4 ;;
esac

rm -rf "$APP" "$ZIP" "$MANIFEST" "$SUMS" "$CODESIGN_LOG" "$VALIDATION_LOG" "$SMOKE_LOG"
mkdir -p "$APP/Contents/MacOS"

echo "source_sha=$SOURCE_SHA"
echo "checkout=$ROOT"
echo "output=$OUT"
echo "bundle=$APP_NAME.app version=$APP_VERSION"

echo "== build Rust helper =="
cargo build --locked --release --bin Saki
HELPER_SRC="$ROOT/target/release/Saki"
test -x "$HELPER_SRC"
cp "$HELPER_SRC" "$APP/Contents/MacOS/Saki-runtime"
chmod +x "$APP/Contents/MacOS/Saki-runtime"

echo "== build GUI =="
cd "$ROOT/desktop"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -trimpath -mod=readonly -buildvcs=true \
  -o "$APP/Contents/MacOS/Saki" .
cd "$ROOT"
chmod +x "$APP/Contents/MacOS/Saki"

echo "== write Info.plist =="
python3 - "$APP/Contents/Info.plist" "$APP_VERSION" <<'PY'
import plistlib
import sys

path, version = sys.argv[1], sys.argv[2]
info = {
    "CFBundleDevelopmentRegion": "en",
    "CFBundleDisplayName": "Saki",
    "CFBundleExecutable": "Saki",
    "CFBundleIdentifier": "dev.saki.desktop",
    "CFBundleInfoDictionaryVersion": "6.0",
    "CFBundleName": "Saki",
    "CFBundlePackageType": "APPL",
    "CFBundleShortVersionString": version,
    "CFBundleVersion": "1",
    "LSMinimumSystemVersion": "11.0",
    "NSHighResolutionCapable": True,
    "NSPrincipalClass": "NSApplication",
}
with open(path, "wb") as handle:
    plistlib.dump(info, handle, fmt=plistlib.FMT_XML, sort_keys=False)
PY

echo "== validate paths, architecture and Info.plist =="
{
  printf 'uname -m: '; uname -m
  printf 'go env: '; go env GOOS GOARCH
  printf 'rustc host: '; rustc -vV | sed -n 's/^host: //p'
  printf 'go version: '; go version
  printf 'cargo: '; cargo --version
  printf 'xcodebuild: '; (xcodebuild -version 2>&1 | head -n 2 | tr '\n' ';' || true); echo
  printf 'sdk: '; (xcrun --sdk macosx --show-sdk-version 2>&1 || true)
  printf 'sw_vers: '; sw_vers -productVersion
  printf 'files:\n'; file "$APP/Contents/MacOS/Saki" "$APP/Contents/MacOS/Saki-runtime" "$APP/Contents/Info.plist"
  printf 'lipo GUI: '; lipo -archs "$APP/Contents/MacOS/Saki"
  printf 'lipo helper: '; lipo -archs "$APP/Contents/MacOS/Saki-runtime"
  plutil -lint "$APP/Contents/Info.plist"
  printf 'CFBundleIdentifier: '; plutil -extract CFBundleIdentifier raw "$APP/Contents/Info.plist"
  printf 'CFBundleExecutable: '; plutil -extract CFBundleExecutable raw "$APP/Contents/Info.plist"
  printf 'CFBundleShortVersionString: '; plutil -extract CFBundleShortVersionString raw "$APP/Contents/Info.plist"
  printf 'CFBundleVersion: '; plutil -extract CFBundleVersion raw "$APP/Contents/Info.plist"
  printf 'LSMinimumSystemVersion: '; plutil -extract LSMinimumSystemVersion raw "$APP/Contents/Info.plist"
  go version -m "$APP/Contents/MacOS/Saki"
} | tee "$VALIDATION_LOG"

test "$(uname -m)" = "arm64"
test "$(go env GOOS)" = "darwin"
test "$(go env GOARCH)" = "arm64"
rustc -vV | grep -q '^host: aarch64-apple-darwin$'
test "$(lipo -archs "$APP/Contents/MacOS/Saki")" = "arm64"
test "$(lipo -archs "$APP/Contents/MacOS/Saki-runtime")" = "arm64"
grep -q 'Mach-O 64-bit executable arm64' "$VALIDATION_LOG"
test "$(plutil -extract CFBundleIdentifier raw "$APP/Contents/Info.plist")" = "dev.saki.desktop"
test "$(plutil -extract CFBundleExecutable raw "$APP/Contents/Info.plist")" = "Saki"
test "$(plutil -extract CFBundleShortVersionString raw "$APP/Contents/Info.plist")" = "$APP_VERSION"
test "$(plutil -extract CFBundleVersion raw "$APP/Contents/Info.plist")" = "1"
test "$(plutil -extract LSMinimumSystemVersion raw "$APP/Contents/Info.plist")" = "11.0"
go version -m "$APP/Contents/MacOS/Saki" | grep -q "vcs.revision=$SOURCE_SHA"
go version -m "$APP/Contents/MacOS/Saki" | grep -q 'vcs.modified=false'
test -x "$APP/Contents/MacOS/Saki"
test -x "$APP/Contents/MacOS/Saki-runtime"

echo "== sign helper, then app (ad-hoc) =="
: > "$CODESIGN_LOG"
echo "helper:" >> "$CODESIGN_LOG"
codesign --force --sign - "$APP/Contents/MacOS/Saki-runtime" >> "$CODESIGN_LOG" 2>&1
codesign --verify --strict "$APP/Contents/MacOS/Saki-runtime" >> "$CODESIGN_LOG" 2>&1
echo "app:" >> "$CODESIGN_LOG"
codesign --force --deep --sign - "$APP" >> "$CODESIGN_LOG" 2>&1
codesign --verify --strict "$APP" >> "$CODESIGN_LOG" 2>&1
codesign --verify --deep --strict "$APP" >> "$CODESIGN_LOG" 2>&1
codesign --verify --strict "$APP/Contents/MacOS/Saki-runtime" >> "$CODESIGN_LOG" 2>&1
{
  echo "app details:"; codesign -dv --verbose=4 "$APP" 2>&1
  echo "helper details:"; codesign -dv --verbose=4 "$APP/Contents/MacOS/Saki-runtime" 2>&1
} >> "$CODESIGN_LOG"

echo "== run existing stdio self-test against signed bundle helper =="
python3 examples/runtime_stdio.py --self-test --binary "$APP/Contents/MacOS/Saki-runtime" 2>&1 | tee "$SMOKE_LOG"
codesign --verify --strict "$APP/Contents/MacOS/Saki-runtime" >> "$CODESIGN_LOG" 2>&1

echo "== archive and manifest =="
ditto -c -k --sequesterRsrc --keepParent "$APP" "$ZIP"

APP_SHA=$(shasum -a 256 "$APP/Contents/MacOS/Saki" | awk '{print $1}')
HELPER_SHA=$(shasum -a 256 "$APP/Contents/MacOS/Saki-runtime" | awk '{print $1}')
PLIST_SHA=$(shasum -a 256 "$APP/Contents/Info.plist" | awk '{print $1}')
ZIP_SHA=$(shasum -a 256 "$ZIP" | awk '{print $1}')

find "$APP" -type f -print | sed "s|^$OUT/||" | sort > "$OUT/files.txt"

SOURCE_SHA="$SOURCE_SHA" CI_EVENT_SHA="$CI_EVENT_SHA" REF="$REF" RUN_URL="$RUN_URL" \
OUT="$OUT" APP_VERSION="$APP_VERSION" APP_SHA="$APP_SHA" HELPER_SHA="$HELPER_SHA" \
PLIST_SHA="$PLIST_SHA" ZIP_SHA="$ZIP_SHA" python3 - "$MANIFEST" <<'PY'
import datetime
import json
import os
import sys
import platform

out = os.environ["OUT"]
manifest = {
    "schema": 1,
    "product": "Saki",
    "candidate": "macos-arm64",
    "app": "Saki.app",
    "source_sha": os.environ["SOURCE_SHA"],
    "checkout_sha": os.environ["SOURCE_SHA"],
    "ci_event_sha": os.environ["CI_EVENT_SHA"],
    "ref": os.environ["REF"],
    "dirty": False,
    "run_url": os.environ["RUN_URL"],
    "runner": {
        "label": "macos-15",
        "arch": platform.machine(),
        "os": platform.platform(),
        "image_os": os.environ.get("ImageOS", "unknown"),
        "image_version": os.environ.get("ImageVersion", "unknown"),
    },
    "toolchain": {
        "go": os.popen("go version").read().strip(),
        "rustc": os.popen("rustc -vV | head -n 3 | tr '\\n' ';'").read().strip(),
        "cargo": os.popen("cargo --version").read().strip(),
        "xcode": os.popen("xcodebuild -version 2>&1 | head -n 2 | tr '\\n' ';'").read().strip(),
        "sdk": os.popen("xcrun --sdk macosx --show-sdk-version 2>&1").read().strip(),
    },
    "bundle": {
        "name": "Saki",
        "identifier": "dev.saki.desktop",
        "version": os.environ["APP_VERSION"],
        "build": "1",
        "minimum_os": "11.0",
        "gui": "Saki.app/Contents/MacOS/Saki",
        "helper": "Saki.app/Contents/MacOS/Saki-runtime",
        "info_plist": "Saki.app/Contents/Info.plist",
    },
    "sha256": {
        "app": os.environ["APP_SHA"],
        "helper": os.environ["HELPER_SHA"],
        "info_plist": os.environ["PLIST_SHA"],
        "zip": os.environ["ZIP_SHA"],
    },
    "signing": {
        "mode": "adhoc",
        "app_verify": "strict+deep",
        "helper_verify": "strict",
        "identifier": "dev.saki.desktop",
    },
    "artifact": "Saki.app.zip",
    "files": [line for line in open(os.path.join(out, "files.txt"), encoding="utf-8").read().splitlines() if line],
    "logs": ["build.log", "codesign.log", "validation.log", "helper-smoke.log"],
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(manifest, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY

(
  cd "$OUT"
  shasum -a 256 \
    "Saki.app/Contents/MacOS/Saki" \
    "Saki.app/Contents/MacOS/Saki-runtime" \
    "Saki.app/Contents/Info.plist" \
    "Saki.app.zip" \
    "manifest.json" > "$SUMS"
)

echo "candidate ready: $APP"
echo "zip: $ZIP"
echo "manifest: $MANIFEST"
echo "source_sha: $SOURCE_SHA"
echo "app_sha256: $APP_SHA"
echo "helper_sha256: $HELPER_SHA"
echo "info_plist_sha256: $PLIST_SHA"
echo "zip_sha256: $ZIP_SHA"
