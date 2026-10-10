#!/usr/bin/env python3
"""Per-user installation and removable file-manager registrations."""
import argparse
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys

APP_ID = "io.github.panktrue.ShareFile"
NAME = "ShareFile: Загрузить и поделиться"


def paths(home=None):
    home = Path(home or Path.home())
    data = Path(os.environ.get("XDG_DATA_HOME", home / ".local/share"))
    config = Path(os.environ.get("XDG_CONFIG_HOME", home / ".config"))
    return {"app": data / "sharefile/client", "launcher": home / ".local/bin/sharefile",
            "desktop": data / f"applications/{APP_ID}.desktop",
            "icon": data / f"icons/hicolor/scalable/apps/{APP_ID}.svg",
            "nautilus": data / "nautilus/scripts" / NAME,
            "nemo": data / "nemo/actions/sharefile.nemo_action",
            "dolphin": data / "kio/servicemenus/sharefile.desktop",
            "autostart": config / f"autostart/{APP_ID}.desktop"}


def desktop_exec(path):
    # Desktop Entry escaping happens twice, for strings and then Exec quoting.
    value = str(path).replace("%", "%%")
    value = "".join("\\" + character if character in ('\\', '"', '`', '$') else character for character in value)
    return '"' + value.replace("\\", "\\\\") + '"'


def write(path, content, mode=0o644):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    path.chmod(mode)


def install(source, home=None, autostart=False, check=True):
    source = Path(source).resolve()
    target = paths(home)
    if check:
        if platform.system() != "Linux":
            raise RuntimeError("Этот пакет устанавливается только в Linux")
        subprocess.run([sys.executable, "-c", "import gi; gi.require_version('Gtk', '3.0'); from gi.repository import Gtk"], check=True)
        if not shutil.which("secret-tool"):
            raise RuntimeError("Установите libsecret-tools / libsecret и включите Secret Service")
        # Running the helper checks architecture before replacing an installed client.
        subprocess.run([str(source / "sharefile-client")], input=b"{}\n", stdout=subprocess.DEVNULL, check=True)
    if not (source / "sharefile-client").is_file():
        raise RuntimeError("В пакете нет ядра передачи. Сначала выполните make linux-build")
    if source == target["app"] or target["app"] in source.parents:
        raise RuntimeError("Запустите установщик из распакованного пакета")
    # Stop a running client before replacing its code. No token appears in argv.
    if check and target["launcher"].exists():
        subprocess.run([str(target["launcher"]), "--quit"], timeout=45, check=True)
    target["app"].mkdir(parents=True, exist_ok=True)
    for name in ("sharefile", "sharefile-client", "install.py", "uninstall.sh", "README.md"):
        shutil.copy2(source / name, target["app"] / name)
    package = target["app"] / "sharefile_linux"
    if package.exists():
        shutil.rmtree(package)
    shutil.copytree(source / "sharefile_linux", package, ignore=shutil.ignore_patterns("__pycache__"))
    for name in ("sharefile", "sharefile-client", "uninstall.sh"):
        (target["app"] / name).chmod(0o755)
    write(target["launcher"], "#!/bin/sh\nexec " + shlex.quote(str(target["app"] / "sharefile")) + ' "$@"\n', 0o755)
    command = desktop_exec(target["launcher"])
    desktop = (f"[Desktop Entry]\nType=Application\nName=ShareFile\nComment=Загрузить файлы и скопировать ссылки\n"
               f"Exec={command} -- %F\nIcon={APP_ID}\nTerminal=false\nCategories=Network;Utility;\n"
               "StartupNotify=true\nActions=Share;UploadClipboard;\n\n[Desktop Action Share]\nName=Загрузить и поделиться\n"
               f"Exec={command} --background -- %F\n"
               f"\n[Desktop Action UploadClipboard]\nName=Из буфера обмена\nExec={command} --clipboard\nIcon={APP_ID}\n")
    write(target["desktop"], desktop)
    write(target["icon"], (source / "resources/sharefile.svg").read_text(encoding="utf-8"))
    # Nautilus passes real argv for local selections. Avoid newline-delimited env variables.
    script = ("#!/bin/sh\n[ \"$#\" -gt 0 ] || exit 0\nexec " + shlex.quote(str(target["launcher"]))
              + ' --background -- "$@"\n')
    write(target["nautilus"], script, 0o755)
    # Nemo quotes each path with the requested Quote character.
    write(target["nemo"], f"[Nemo Action]\nName={NAME}\nComment=Загрузить файлы и скопировать ссылки\n"
          f"Exec={command} --background -- %F\nIcon-Name={APP_ID}\nSelection=notnone\nExtensions=any;\n"
          "Quote=double\nUriScheme=file\n")
    write(target["dolphin"], "[Desktop Entry]\nType=Service\nMimeType=application/octet-stream;inode/directory;\n"
          "X-KDE-ServiceTypes=KonqPopupMenu/Plugin\nX-KDE-Protocols=file\nActions=Share;\n\n"
          f"[Desktop Action Share]\nName={NAME}\nIcon={APP_ID}\nExec={command} --background -- %F\n", 0o755)
    if autostart:
        write(target["autostart"], f"[Desktop Entry]\nType=Application\nName=ShareFile\nExec={command} --background\n"
              f"Icon={APP_ID}\nTerminal=false\nX-GNOME-Autostart-enabled=true\n")
    if check and shutil.which("update-desktop-database"):
        subprocess.run(["update-desktop-database", str(target["desktop"].parent)], check=False)
    return target


def uninstall(home=None, check=True):
    target = paths(home)
    if check and target["launcher"].exists():
        subprocess.run([str(target["launcher"]), "--quit"], timeout=45, check=True)
    for key, path in target.items():
        if key != "app":
            path.unlink(missing_ok=True)
    if target["app"].exists():
        shutil.rmtree(target["app"])
    # Preserve durable queue, downloaded files and Secret Service credentials.


def main():
    parser = argparse.ArgumentParser(description="Установка ShareFile для текущего пользователя")
    parser.add_argument("--uninstall", action="store_true")
    parser.add_argument("--autostart", action="store_true")
    options = parser.parse_args()
    try:
        if options.uninstall:
            uninstall()
            print("ShareFile удалён. Очередь и системные секреты сохранены; см. docs/linux.md для полного удаления.")
        else:
            install(Path(__file__).resolve().parent, autostart=options.autostart)
            print("ShareFile установлен. Откройте его в меню приложений. Перезапустите файловый менеджер для появления действия.")
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print("Не удалось установить/удалить ShareFile: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
