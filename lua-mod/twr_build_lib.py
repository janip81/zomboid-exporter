"""Shared build logic for ThoseWhoRemain's build-dev-twr.py and
build-workshop-twr.py -- kept in one place so the dev and production
build paths can never silently drift apart from each other.

See build-dev-twr.py's module docstring for why shared/TWR and
client/TWR are wholesale-replaced while scripts/ and translate/*/ are
copied file-by-file instead.
"""
import shutil
from pathlib import Path

LUA_MOD_DIR = Path(__file__).resolve().parent
SRC = LUA_MOD_DIR / "src" / "ThoseWhoRemain"


def build_package(root):
    if (root / "42" / "media" / "lua" / "shared" / "TWR").exists():
        shutil.rmtree(root / "42" / "media" / "lua" / "shared" / "TWR")
    shutil.copytree(SRC / "shared" / "TWR", root / "42" / "media" / "lua" / "shared" / "TWR")

    if (root / "42" / "media" / "lua" / "client" / "TWR").exists():
        shutil.rmtree(root / "42" / "media" / "lua" / "client" / "TWR")
    shutil.copytree(SRC / "client" / "TWR", root / "42" / "media" / "lua" / "client" / "TWR")

    if (root / "42" / "media" / "lua" / "server" / "TWR").exists():
        shutil.rmtree(root / "42" / "media" / "lua" / "server" / "TWR")
    shutil.copytree(SRC / "server" / "TWR", root / "42" / "media" / "lua" / "server" / "TWR")

    scripts_dest = root / "42" / "media" / "scripts"
    scripts_dest.mkdir(parents=True, exist_ok=True)
    for f in (SRC / "scripts").glob("*.txt"):
        shutil.copy2(f, scripts_dest / f.name)

    for lang_dir in (SRC / "translate").iterdir():
        if not lang_dir.is_dir():
            continue
        translate_dest = root / "42" / "media" / "lua" / "shared" / "Translate" / lang_dir.name
        translate_dest.mkdir(parents=True, exist_ok=True)
        for f in lang_dir.glob("*.json"):
            shutil.copy2(f, translate_dest / f.name)
