#!/usr/bin/env bash
# Rebuilds a mod's Workshop package and uploads it via steamcmd's
# workshop_build_item, with a release note attached to the update.
#
# One-time setup required first -- see lua-mod/WORKSHOP_UPLOAD.md.
#
# Usage:
#   ./workshop_upload.sh <ExporterLog|ThoseWhoRemain> "release note text"
#   ./workshop_upload.sh <ExporterLog|ThoseWhoRemain> -f path/to/notes.txt
#
# Requires STEAM_USER to be set in the environment (your Steam login
# name). steamcmd will reuse the cached login session from the one-time
# interactive setup -- no password/Steam Guard prompt on normal runs.

set -euo pipefail

APPID=108600  # Project Zomboid

MOD="${1:-}"
shift || true

if [ -z "$MOD" ]; then
    echo "Usage: $0 <ExporterLog|ThoseWhoRemain> \"release note\"" >&2
    echo "       $0 <ExporterLog|ThoseWhoRemain> -f path/to/notes.txt" >&2
    exit 1
fi

if [ "${1:-}" = "-f" ]; then
    NOTES_FILE="${2:-}"
    if [ -z "$NOTES_FILE" ] || [ ! -f "$NOTES_FILE" ]; then
        echo "Error: -f requires an existing file path" >&2
        exit 1
    fi
    CHANGENOTE="$(cat "$NOTES_FILE")"
else
    CHANGENOTE="$*"
fi

if [ -z "$CHANGENOTE" ]; then
    echo "Error: release note text is required" >&2
    exit 1
fi

if [ -z "${STEAM_USER:-}" ]; then
    echo "Error: STEAM_USER environment variable is not set" >&2
    echo "Export it first: export STEAM_USER=your_steam_login" >&2
    exit 1
fi

STEAMCMD="${STEAMCMD_PATH:-/opt/steamcmd/steamcmd.sh}"
if [ ! -x "$STEAMCMD" ]; then
    echo "Error: steamcmd not found/executable at $STEAMCMD" >&2
    echo "Set STEAMCMD_PATH, or see lua-mod/WORKSHOP_UPLOAD.md for setup." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

case "$MOD" in
    ExporterLog)
        BUILD_SCRIPT="build-workshop.py"
        WORKSHOP_DIR="ExporterLog-Workshop"
        ;;
    ThoseWhoRemain)
        BUILD_SCRIPT="build-workshop-twr.py"
        WORKSHOP_DIR="ThoseWhoRemain-Workshop"
        ;;
    *)
        echo "Error: unknown mod '$MOD' (expected ExporterLog or ThoseWhoRemain)" >&2
        exit 1
        ;;
esac

WORKSHOP_TXT="$SCRIPT_DIR/$WORKSHOP_DIR/workshop.txt"
if [ ! -f "$WORKSHOP_TXT" ]; then
    echo "Error: $WORKSHOP_TXT not found" >&2
    exit 1
fi

WORKSHOP_ID="$(grep -E '^id=' "$WORKSHOP_TXT" | head -1 | cut -d= -f2)"
if [ -z "$WORKSHOP_ID" ]; then
    echo "Error: could not read id= from $WORKSHOP_TXT" >&2
    exit 1
fi

CONTENT_FOLDER="$SCRIPT_DIR/$WORKSHOP_DIR/Contents"
PREVIEW_FILE="$SCRIPT_DIR/$WORKSHOP_DIR/preview.png"
if [ ! -f "$PREVIEW_FILE" ]; then
    echo "Error: preview image not found at $PREVIEW_FILE" >&2
    exit 1
fi

echo "==> Rebuilding $MOD Workshop package ($BUILD_SCRIPT)..."
python3 "$SCRIPT_DIR/$BUILD_SCRIPT"

VDF_FILE="$(mktemp /tmp/workshop_upload_XXXXXX.vdf)"
trap 'rm -f "$VDF_FILE"' EXIT

# steamcmd's VDF parser doesn't support escaping quotes -- strip any
# double quotes from the changenote so a malformed note can't break the
# VDF or get truncated silently.
SAFE_CHANGENOTE="${CHANGENOTE//\"/}"

cat > "$VDF_FILE" <<EOF
"workshopitem"
{
    "appid"          "$APPID"
    "publishedfileid" "$WORKSHOP_ID"
    "contentfolder"  "$CONTENT_FOLDER"
    "previewfile"    "$PREVIEW_FILE"
    "visibility"     "3"
    "changenote"     "$SAFE_CHANGENOTE"
}
EOF
# FIX 2026-08-13: visibility was "2" here, which is Steam's
# ERemoteStoragePublishedFileVisibility Private (0=Public, 1=FriendsOnly,
# 2=Private, 3=Unlisted) -- NOT Unlisted as workshop.txt/WORKSHOP_UPLOAD.md
# claim. CONFIRMED live: this broke both a connecting client
# (ConnectToServerState: CheckItemState -> Fail) and even an anonymous
# `steamcmd +workshop_download_item` (Access Denied) once an upload had
# run with the old value -- a private item is not fetchable by anyone but
# the owning account. Corrected to 3.

echo "==> Uploading $MOD (Workshop id $WORKSHOP_ID) via steamcmd..."
echo "==> Release note: $SAFE_CHANGENOTE"

"$STEAMCMD" +login "$STEAM_USER" +workshop_build_item "$VDF_FILE" +quit

echo "==> Done. Verify at https://steamcommunity.com/sharedfiles/filedetails/?id=$WORKSHOP_ID"
