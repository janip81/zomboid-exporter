# Uploading mod updates to Steam Workshop from the CLI

`workshop_upload.sh` rebuilds a mod's Workshop package and uploads it via
`steamcmd`, attaching a release note to the update. One-time setup is
required first (needs your Steam password + Steam Guard code, so it must
be done interactively).

## One-time setup

1. Install the 32-bit compat libraries `steamcmd` needs (Rocky Linux 9):

   ```bash
   sudo dnf install -y epel-release
   sudo /usr/bin/crb enable
   sudo dnf install -y glibc.i686 libstdc++.i686 ncurses-libs.i686
   ```

2. Download and extract `steamcmd`:

   ```bash
   sudo mkdir -p /opt/steamcmd
   sudo chown "$USER" /opt/steamcmd
   cd /opt/steamcmd
   curl -sL https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz | tar xz
   ```

3. Log in interactively **once**, so `steamcmd` caches your session
   (avoids a password/Steam Guard prompt on every future upload):

   ```bash
   /opt/steamcmd/steamcmd.sh +login <your_steam_username> +quit
   ```

   It will prompt for your password, then a Steam Guard code sent to
   your email/Steam Mobile app. Enter both. If it succeeds, subsequent
   runs won't prompt again unless the session expires or you log out
   elsewhere.

4. Export your Steam username in your shell profile (`~/.bashrc` or
   similar), so the upload script doesn't need it passed every time:

   ```bash
   export STEAM_USER=your_steam_username
   ```

That's it — steps 1-3 are one-time. After this, uploads are a single
command.

## Uploading an update

```bash
cd /opt/git/app-development/zomboid-exporter/lua-mod

# Release note as a literal string:
./workshop_upload.sh ExporterLog "Fix indoor/outdoor streak tracking for vehicles"

# Or from a file (e.g. paste a multi-line changelog entry):
./workshop_upload.sh ThoseWhoRemain -f /tmp/release-notes.txt
```

The script:
1. Reruns the mod's build script (`build-workshop.py` or
   `build-workshop-twr.py`) to regenerate `Contents/` from `src/` --
   always uploads current source, never stale content.
2. Reads the mod's existing Workshop item ID from its `workshop.txt`
   (`ExporterLog-Workshop/workshop.txt` / `ThoseWhoRemain-Workshop/
   workshop.txt`) -- always updates the SAME existing item, never
   creates a new one.
3. Generates a temporary `.vdf` config pointing `steamcmd` at the
   content folder, preview image, and your release note.
4. Runs `steamcmd +login $STEAM_USER +workshop_build_item <vdf> +quit`.

Verify the update landed at the URL the script prints at the end
(`https://steamcommunity.com/sharedfiles/filedetails/?id=<workshop-id>`)
-- the Workshop page's "Change Notes" tab should show your release note.

## Notes

- Visibility is always kept at `unlisted` (matches both mods'
  `workshop.txt`) -- the upload never changes visibility.
- If `steamcmd` prompts for a password/Steam Guard code during a normal
  upload run, your cached session expired -- redo step 3 above.
- `STEAMCMD_PATH` env var overrides the default `/opt/steamcmd/
  steamcmd.sh` location if you installed it elsewhere.
