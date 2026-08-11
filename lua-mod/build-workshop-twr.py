#!/usr/bin/env python3
"""Copy the authoritative src/ThoseWhoRemain tree into both production
build outputs:

  1. ThoseWhoRemain/42/...            -- plain mod-folder copy, used for
                                          local pre-Workshop testing
  2. ThoseWhoRemain-Workshop/Contents/mods/ThoseWhoRemain/42/...
                                       -- Steam Workshop upload staging copy

Both are "the production build" -- same TWR code, just packaged for two
different distribution paths, same pattern as ExporterLog's
build-workshop.py. src/ThoseWhoRemain is the ONLY place TWR code should
ever be hand-edited; this script (and build-dev-twr.py) are the only
things that should write into either destination below -- never edit
files there directly, they will be silently overwritten on the next
build.

Steam's own downloaded Workshop copy is never touched by this script and
is never part of this repo.

mod.info / workshop.txt / preview.png are left untouched -- only the
Lua/script/translate tree is replaced.
"""
import sys

from twr_build_lib import LUA_MOD_DIR, SRC, build_package

DESTINATIONS = [
    LUA_MOD_DIR / "ThoseWhoRemain",
    LUA_MOD_DIR / "ThoseWhoRemain-Workshop" / "Contents" / "mods" / "ThoseWhoRemain",
]


def main():
    if not SRC.is_dir():
        print(f"error: source tree not found: {SRC}", file=sys.stderr)
        sys.exit(1)

    for root in DESTINATIONS:
        build_package(root)
        print(f"built production package: {SRC} -> {root}")


if __name__ == "__main__":
    main()
