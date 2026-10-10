"""Build the Finder layout without UI automation or AppleScript permissions."""

import argparse
from pathlib import Path
import subprocess

import dmgbuild


def check_contents(mount_point, _settings):
    root = Path(mount_point)
    visible = {entry.name for entry in root.iterdir() if not entry.name.startswith(".")}
    if visible != {"ShareFile.app", "Applications"}:
        raise RuntimeError(f"Unexpected DMG contents: {sorted(visible)}")
    if not (root / "Applications").is_symlink() or (root / "Applications").readlink() != Path("/Applications"):
        raise RuntimeError("Applications must link to /Applications")
    subprocess.run(["codesign", "--verify", "--deep", "--strict", str(root / "ShareFile.app")], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("app", type=Path)
    parser.add_argument("background", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    dmgbuild.build_dmg(str(args.output), "ShareFile", settings={
        "format": "UDZO",
        "filesystem": "HFS+",
        "files": [str(args.app)],
        "symlinks": {"Applications": "/Applications"},
        "background": str(args.background),
        "icon_locations": {"ShareFile.app": (184, 238), "Applications": (536, 238)},
        # Finder's saved window size includes the 32-point title bar.
        "window_rect": ((200, 160), (720, 482)),
        "default_view": "icon-view",
        "icon_size": 104,
        "text_size": 13,
        "grid_spacing": 80,
        "show_toolbar": False,
        "show_sidebar": False,
        "show_status_bar": False,
        "show_pathbar": False,
        "show_tab_view": False,
        "create_hook": check_contents,
    })


if __name__ == "__main__":
    main()
