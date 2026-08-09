#!/usr/bin/env python3
"""Copy the authoritative src/ExporterLog Lua tree into the local
single-player dev mod package (ExporterLog_Dev).

src/ExporterLog is the ONLY place tracker Lua should ever be hand-edited.
This script (and build-workshop.py) are the only things that should ever
write into ExporterLog_Dev/42/media/lua/server/ExporterLog -- never edit
files there directly, they will be silently overwritten on the next build.

mod.info is left untouched -- only the Lua module tree is replaced.
"""
import shutil
import sys
from pathlib import Path

LUA_MOD_DIR = Path(__file__).resolve().parent
SRC = LUA_MOD_DIR / "src" / "ExporterLog"
DEST = LUA_MOD_DIR / "ExporterLog_Dev" / "42" / "media" / "lua" / "server" / "ExporterLog"


def main():
    if not SRC.is_dir():
        print(f"error: source tree not found: {SRC}", file=sys.stderr)
        sys.exit(1)

    if DEST.exists():
        shutil.rmtree(DEST)
    shutil.copytree(SRC, DEST)

    print(f"built dev package: {SRC} -> {DEST}")


if __name__ == "__main__":
    main()
