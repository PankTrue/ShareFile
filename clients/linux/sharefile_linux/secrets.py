from .localization import tr
"""Secret Service via libsecret's CLI; no plaintext fallback."""
import shutil
import subprocess

from .bridge import ClientError


class SecretStore:
    def _run(self, operation, origin, kind, value=None):
        executable = shutil.which("secret-tool")
        if not executable:
            raise ClientError(tr("Установите secret-tool (libsecret) и включите GNOME Keyring или KWallet с Secret Service"))
        args = [executable, operation]
        if operation == "store":
            args.append("--label=ShareFile · " + kind)
        args += ["application", "io.github.panktrue.ShareFile", "origin", origin, "kind", kind]
        try:
            result = subprocess.run(args, input=value, text=True, capture_output=True, timeout=120)
        except (OSError, subprocess.TimeoutExpired):
            raise ClientError(tr("Системное хранилище секретов недоступно или заблокировано")) from None
        if result.returncode != 0:
            if operation in ("lookup", "clear") and result.returncode == 1 and not result.stderr.strip():
                return ""
            raise ClientError(tr("Системное хранилище секретов недоступно. Разблокируйте GNOME Keyring или KWallet"))
        return result.stdout.rstrip("\n") if operation == "lookup" else ""

    def read(self, origin, kind="device"):
        return self._run("lookup", origin, kind)

    def write(self, origin, value, kind="device"):
        self._run("store", origin, kind, value)

    def delete(self, origin, kind="device"):
        self._run("clear", origin, kind)
