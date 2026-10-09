# ShareFile

ShareFile is a lightweight file server for a Keenetic Ultra KN-1811 running Entware. It combines a Go backend, SQLite metadata, a React Web UI, and native macOS and Windows clients. Files are stored on a dedicated external drive and transferred as streams, with resumable uploads and HTTP Range downloads.

The default router deployment provides direct HTTPS access at **https://192.168.1.1:8081**, with automatic startup through Entware and automatic process restart. SSH is used for installation and maintenance; browsing and file transfers work directly over LAN or VPN.

## Install on the router from GitHub

### Requirements

- An ARM64 router: `uname -m` must print `aarch64`.
- Entware installed and its `/opt` filesystem mounted read-write with executable support.
- Linux kernel 3.2 or newer, as required by the project's Go version.
- Internet access from the router to GitHub and the Entware package repository.
- A published GitHub Release containing `sharefile-router.tar.gz`, its `.sha256` file, and `install-sharefile.sh`.
- An interactive root shell in Entware. When connecting from a computer, use `ssh -t -p 222 root@192.168.1.1`, adjusting the SSH port if necessary.

The packaged defaults match this router's two-drive layout:

- Entware, the executable, Web UI, and logs: `/opt` on `/dev/sdb`.
- File contents and SQLite: `/tmp/mnt/745ee3e5-6027-4b29-8e14-3fdc29729706/sharefile` on the second ext4 drive.
- Storage identity: `sharefile-745ee3e5-6027-4b29-8e14-3fdc29729706`.
- LAN/VPN HTTPS address: `https://192.168.1.1:8081`.

The data drive may be mounted with `noexec`; the application executable stays on `/opt`. The installer does not format drives or import existing files. For a different layout on the first installation, adjust the deployment templates and installation scripts before building your release. Once running, select a disk, set the storage directory, and edit all configuration fields in **Administration → Settings**. Save a draft, then apply it to restart the listeners; changing storage copies the files and database while preserving the original.

### Download and install

Run these commands **inside the router's Entware shell**:

```sh
opkg update
opkg install curl ca-bundle coreutils-sha256sum

curl --fail --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/PankTrue/ShareFile/releases/latest/download/install-sharefile.sh \
  -o /tmp/install-sharefile.sh

sh /tmp/install-sharefile.sh
```

The single installer downloads the latest stable release, verifies the archive's SHA-256 checksum, and installs it. It stages downloads on `/opt`, installs the supervisor and TLS tools, initializes storage, prompts for an administrator password, registers `/opt/etc/init.d/S90sharefile`, and starts the server.

Use a password of at least 12 characters. Keep the script's stdin attached to your terminal: download it to a file as shown above so the password prompt remains interactive.

To install a specific release instead:

```sh
sh /tmp/install-sharefile.sh --version v0.1.0
```

Replace `v0.1.0` with an existing release tag. If no release has been published yet, follow [Publish a GitHub release](#publish-a-github-release) first; the download URLs become available after the workflow finishes successfully.

### Trust the HTTPS certificate

The router creates its own local CA and a server certificate containing `IP:192.168.1.1` in its Subject Alternative Name. Add the public CA certificate to each client once. Private keys stay on the router under `/opt/etc/sharefile` with permissions `600`.

On your Mac:

```sh
ssh -p 222 root@192.168.1.1 \
  'cat /opt/etc/sharefile/lan-ca.pem' > /tmp/sharefile-lan-ca.pem

security add-trusted-cert -r trustRoot -p ssl \
  -k "$HOME/Library/Keychains/login.keychain-db" \
  /tmp/sharefile-lan-ca.pem

curl --fail --cacert /tmp/sharefile-lan-ca.pem \
  https://192.168.1.1:8081/readyz
```

macOS asks you to authorize the trust change. You can also import the certificate into **Keychain Access → login**, open it, and set **Trust → Secure Sockets Layer (SSL) → Always Trust**. See [Apple's certificate trust instructions](https://support.apple.com/guide/keychain-access/change-the-trust-settings-of-a-certificate-kyca11871/mac).

Open **https://192.168.1.1:8081** and sign in as `admin`. The same address serves files, sharing links, and administrator tools; administrator operations still require an administrator account. No SSH tunnel is needed. For VPN access, the client must have a route to `192.168.1.1`.

The CA is valid for ten years; the server certificate is valid for one year. Re-running `enable-lan.sh` from a release package renews a server certificate with less than 30 days remaining while keeping the CA. Certificate renewal is not scheduled automatically.

## Update an existing installation

Download and run `install-sharefile.sh` using the same commands. If `/opt/etc/sharefile.json` already exists, the installer selects the update path automatically.

Updates replace the executable, Web UI, and service scripts. They preserve the existing JSON configuration, TLS certificates, storage identity, accounts, and stored files. The updater stops the service before replacement, waits for `/readyz`, and restores the previous application files if startup fails.

Previous application files and a configuration copy are retained under:

```text
/opt/var/backups/sharefile/release.XXXXXX/
```

These are application backups, not full data backups. Keep an offline storage backup before an upgrade that changes the database schema. Automatic updates require the standard `web_dir` at `/opt/share/sharefile/web`.

For an older installation that still uses an SSH tunnel, `enable-lan.sh` in the unpacked release switches it to LAN HTTPS while preserving its storage settings. Ordinary updates preserve your existing network configuration.

## Service management

Run on the router:

```sh
/opt/etc/init.d/S90sharefile start
/opt/etc/init.d/S90sharefile check
/opt/etc/init.d/S90sharefile restart
/opt/etc/init.d/S90sharefile stop
```

The executable init script is picked up by Entware at boot. The supervisor restarts the backend after a process failure, including startup failures while the data drive is unavailable. The service uses `GOMEMLIMIT=96MiB` and `GOGC=50`; the memory limit is a soft Go heap target, not an RSS limit.

A manual stop lasts until the next start, including Entware startup after reboot. To disable automatic startup:

```sh
/opt/etc/init.d/S90sharefile stop
chmod 644 /opt/etc/init.d/S90sharefile
```

To enable it again:

```sh
chmod 755 /opt/etc/init.d/S90sharefile
/opt/etc/init.d/S90sharefile start
```

Logs: `/opt/var/log/sharefile/server.jsonl`, with one rotated file, each limited to 1 MiB. Health endpoints: `/healthz` for process liveness and `/readyz` for storage/database readiness.

## Publish a GitHub release

The [router workflow](.github/workflows/router-release.yml) runs on pushes to `master`/`main`, pull requests, and manual dispatch. It builds the Linux ARM64 executable and React assets, checks the backend and Web UI, exercises the installer, and uploads an Actions artifact named `sharefile-router`.

A pushed `v*` tag also publishes a GitHub Release with three downloadable assets:

- `sharefile-router.tar.gz`: server, Web UI, configuration templates, and Entware scripts.
- `sharefile-router.tar.gz.sha256`: archive checksum.
- `install-sharefile.sh`: the standalone download/install/update script.

Commit and push the source, installer, and workflow changes first. Then choose an unused version tag, for example:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Wait for **Actions → Router build and release** to finish, then check the repository's **Releases** page. Tags containing a hyphen, such as `v0.2.0-rc.1`, are published as prereleases and are available through `--version`; the default installer selects the latest stable release.

Publishing uses the repository's `GITHUB_TOKEN` with `contents: write` only in the release job. No personal access token is needed for the workflow. GitHub release downloads in the installation command assume the repository/releases are public. See the [GitHub CLI release documentation](https://cli.github.com/manual/gh_release_create).

## Build locally

Build requirements: Go **1.26.8**, Node.js **24.21.0**, npm, and Python **3.11+**. Go, Node.js, and Python are not required on the router; the router receives a static binary and compiled Web UI assets. SQLite does not require cgo or a separate database service.

From the repository root:

```sh
make router-release
```

Output files are written to `backend/bin/`. If your Go executable is outside `PATH`, pass it explicitly:

```sh
make GO=/absolute/path/to/go router-release
```

Individual build and validation commands:

```sh
make build
make arm64
make check
make web-check
python3 scripts/test-router-installer.py
make vulnerabilities
```

To install a locally built package, transfer and unpack it into a fresh staging directory on `/opt`, then run `first-install.sh` for a new installation or `update.sh` for an existing one. Detailed router, KeenDNS, backup, and restore procedures are in [docs/deployment.md](docs/deployment.md).

## Local development

```sh
make build web-build
python3 scripts/configure-local.py
backend/bin/sharefile init-storage -config local-config.json
backend/bin/sharefile admin-create -config local-config.json -username admin
backend/bin/sharefile serve -config local-config.json
```

The public development interface is `http://127.0.0.1:8080`; the administrator interface is `http://127.0.0.1:8081`. `insecure_local=true` is allowed only with loopback listeners and local HTTP origins. The local example stores data in `/tmp/sharefile-local`, which the operating system may clean up.

For React hot reload, run `npm run dev` and `npm run dev:admin` in separate terminals under `web/`, with the backend running. Open `http://127.0.0.1:5173` or `http://127.0.0.1:5174`. Development Vite proxies requests to the backend; deployed UI and API share one origin.

```sh
make web-check
cd web
npx playwright install chromium
cd ..
make browser-check
python3 scripts/smoke-test.py
```

Browser and smoke checks use temporary storage and test accounts, without changing router data. Browser artifacts are written to the ignored `web/test-results/` directory.

## Features and API

- Web configuration of storage disk/directory, network, TLS paths, transfer limits, import sources, UI and logging, with persisted drafts and safe offline storage copying.
- Administrator-managed users and groups, personal roots and `Uploads` folders, shared folders, and inherited reader/editor/manager permissions.
- Streaming tus uploads with durable offsets, resumable transfers, and HTTP Range downloads. Default concurrency: two uploads and four downloads; default chunk size: 16 MiB.
- Whole-directory uploads from Finder, Windows Explorer, Linux file managers and the browser. Nested paths, duplicate basenames and empty folders are preserved; each directory is one resumable queue item with one public link created after every file completes. All clients offer a folder picker; use browser drag-and-drop to include empty folders. Limit: 10,000 entries and 128 nested path components per directory.
- File and live-folder sharing links with optional passwords and expiration. Public folders include nested navigation, photo previews (JPEG, PNG, GIF, WebP, BMP), escaped UTF-8 text previews, and separate download buttons. Text previews show the first 256 KiB; other formats can be downloaded. Revocation, permission loss, and file replacement invalidate affected links.
- Seven-day trash, restoration, crash recovery, and explicit copy-import from configured directories. The storage directory must be reserved for ShareFile; avoid writing to it through SMB, FTP, or other applications.
- Browser sessions with Secure/HttpOnly/SameSite cookies and CSRF protection; separately revocable device tokens approved through a browser.
- Dedicated storage markers, a process lock, bounded logs, and offline backup/restore. Missing or replaced storage causes requests to fail; the server does not create a fallback data directory.

There is no application-level file size limit or per-user quota. Free disk space and the configured reserve constrain uploads. Browser clients require sizes within JavaScript's safe integer range; other clients can use the API's int64 byte sizes.

The full API contract is [api/openapi.json](api/openapi.json). Authenticate with `POST /api/v1/auth/login`; cookie-authenticated mutations require `X-CSRF-Token`. Devices use `Authorization: Bearer <device-token>`. Administrative routes exist only on the private listener; the public listener returns 404 for them.

Uploads use `POST /api/v1/uploads` to create a session, `HEAD` to retrieve the durable offset, and sequential `PATCH` chunks. The backend also accepts `POST` with `X-HTTP-Method-Override: PATCH`. Downloads support Range, ETag, If-Range, and HEAD. Share URLs are returned once; persist them on the client.

## macOS client

The native SwiftUI/AppKit client provides browser-assisted connection, a Keychain token, a resumable upload queue, and a Finder Service. Select or drop files and directories; directory uploads preserve empty subfolders and produce one link for the entire tree. Finder uploads run in the background by default, copy the completed sharing links, and send one completion notification per selection. Settings include separate switches for silent Finder uploads and completion notifications, both enabled by default. macOS asks for notification permission on the first completion; uploads and clipboard copying continue if permission is denied. Open the window or settings through the ShareFile menu bar icon. Initial connection setup still opens a window when needed.

It requires macOS 13 or later. The current package uses ad-hoc signing.

```sh
make macos-build
make desktop-check
make macos-check
make desktop-smoke
```

The package is `clients/macos/build/ShareFile-macOS-universal.zip`. See [docs/macos.md](docs/macos.md).

## Linux client

The separate GTK 3/Python client reuses the Go transfer core. It provides browser-assisted pairing, Secret Service credentials, a durable resumable queue, file/folder drag-and-drop and selection, link settings/revocation, and background actions for Nautilus, Nemo and Dolphin. Each uploaded directory preserves its tree and produces one folder link. A selection produces one clipboard update and one completion notification. Public URLs and credentials are excluded from checkpoints. Closing the window keeps the client in the background; relaunch it to view the queue.

Per-user x86_64 and ARM64 packages include the static transfer core. Install Python 3.10+, PyGObject/GTK 3 and libsecret, extract the package and run `python3 install.py`. See [docs/linux.md](docs/linux.md) for dependencies, installation, desktop integration and validation limits.

```sh
make linux-build
make linux-check
make linux-smoke
```

## Windows client

The C#/.NET 10 client uses WPF, CommunityToolkit.Mvvm, and Microsoft dependency injection. It provides browser-assisted pairing, Windows Credential Manager storage, a durable resumable queue, file/folder drag-and-drop and selection, a tray icon, and an Explorer **Send to** shortcut. A directory preserves empty subfolders and produces one folder link. Explorer selections upload in the background, copy all resulting links together, and produce one completion notification. Public URLs and credentials are excluded from queue checkpoints.

Self-contained x64 and ARM64 packages include the runtime. Extract the entire ZIP and run `Install.cmd`; installation is per-user and does not need administrator rights. Windows packages are currently unsigned. On Windows, build with `clients/windows/build.ps1`; the **Windows client** workflow produces the **ShareFile-Windows** Actions artifact.

```sh
make windows-compile
make windows-check
make windows-smoke
```

These commands require .NET SDK 10. The smoke test also builds the Go backend and uses temporary test storage. Cross-compilation and C# protocol tests have passed on macOS; native Windows execution and Explorer integration still require Windows validation. See [docs/windows.md](docs/windows.md).

## Project documentation

- [Architecture and agreed requirements](docs/architecture.md).
- [Implementation plan](docs/implementation-plan.md).
- [Web UI development and transfer behavior](docs/web-ui.md).
- [Windows client, installation and validation](docs/windows.md).
- [Linux client, installation and validation](docs/linux.md).
- [Deployment, KeenDNS, backup and restore](docs/deployment.md).
- [Validation results and remaining device checks](docs/validation.md).

The detailed documents currently use Russian. Local backend, Web UI, TLS, and installer checks have passed; installation on the real router, boot/hotplug behavior, large-file transfers, VPN access, and external KeenDNS proxy behavior still need device verification.
