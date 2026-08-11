#!/usr/bin/env python3
"""Copy the authoritative src/ThoseWhoRemain Lua/script/translate tree
into the local single-player dev mod package (ThoseWhoRemain_Dev).

src/ThoseWhoRemain is the ONLY place TWR code should ever be hand-edited.
This script (and build-workshop-twr.py) are the only things that should
ever write into ThoseWhoRemain_Dev/42/media/... -- never edit files there
directly, they will be silently overwritten on the next build.

Build logic itself lives in twr_build_lib.py, shared with
build-workshop-twr.py so the dev and production build paths can't drift
apart from each other.

mod.info is left untouched -- only the Lua/script/translate tree is
replaced.
"""
import sys

from twr_build_lib import LUA_MOD_DIR, SRC, build_package


def main():
    if not SRC.is_dir():
        print(f"error: source tree not found: {SRC}", file=sys.stderr)
        sys.exit(1)

    root = LUA_MOD_DIR / "ThoseWhoRemain_Dev"
    build_package(root)
    print(f"built dev package: {SRC} -> {root}")


if __name__ == "__main__":
    main()
