#!/usr/bin/env python3
"""Copy the authoritative src/ExporterLog Lua tree into both production
build outputs:

  1. ExporterLog/42/...            -- plain mod-folder copy, used for
                                       local pre-Workshop testing (scp'd
                                       onto a client's mods/ folder)
  2. ExporterLog-Workshop/Contents/mods/ExporterLog/42/...
                                    -- Steam Workshop upload staging copy

Both are "the production build" -- same tracker code, just packaged for
two different distribution paths. src/ExporterLog is the ONLY place
tracker Lua should ever be hand-edited; this script (and build-dev.py)
are the only things that should write into either destination below --
never edit files there directly, they will be silently overwritten on
the next build.

Steam's own downloaded Workshop copy (steamapps/workshop/content/108600/
<id>/) is never touched by this script and is never part of this repo --
it's a disposable runtime copy Steam manages, not a build target.

mod.info / workshop.txt / preview.png are left untouched -- only the Lua
module tree is replaced.
"""
import shutil
import sys
from pathlib import Path

LUA_MOD_DIR = Path(__file__).resolve().parent
SRC = LUA_MOD_DIR / "src" / "ExporterLog"

DESTINATIONS = [
    LUA_MOD_DIR / "ExporterLog" / "42" / "media" / "lua" / "server" / "ExporterLog",
    LUA_MOD_DIR / "ExporterLog-Workshop" / "Contents" / "mods" / "ExporterLog" / "42" / "media" / "lua" / "server" / "ExporterLog",
]


def main():
    if not SRC.is_dir():
        print(f"error: source tree not found: {SRC}", file=sys.stderr)
        sys.exit(1)

    for dest in DESTINATIONS:
        if dest.exists():
            shutil.rmtree(dest)
        shutil.copytree(SRC, dest)
        print(f"built production package: {SRC} -> {dest}")


if __name__ == "__main__":
    main()
