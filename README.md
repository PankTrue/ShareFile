# ShareFile

ShareFile is a lightweight file server for a Keenetic Ultra KN-1811 running Entware. It combines a Go backend, SQLite metadata, a React Web UI, and separate macOS, Windows and Linux clients. Files are stored on a dedicated external drive and transferred as streams, with resumable uploads and HTTP Range downloads.

The default router deployment provides direct HTTPS access at **https://192.168.1.1:8081**, with automatic startup through Entware and automatic process restart. SSH is used for installation and maintenance; browsing and file transfers work directly over LAN or VPN.

## Implemented features

The following features are implemented in the current backend, Web UI and desktop clients. Platform and router validation results are tracked separately in [docs/validation.md](docs/validation.md).

### File and folder management

- **Personal storage:** each account receives a private root and an `Uploads` folder. **Мои файлы** opens personal storage; **Доступные папки** lists other accessible personal and shared folders.
- **Folder tree:** create nested folders, navigate with breadcrumbs, and browse files and folders in pages of up to 100 entries. Administrators can create shared root folders.
- **File operations:** upload, download, rename, move, explicitly replace, and send files or entire folder trees to the trash. Available actions depend on the current account's permissions.
- **Name conflicts:** ordinary uploads and new folders receive a numeric suffix such as `report (1).pdf`. Replacement is a separate explicit action; it keeps the original visible name, moves the old object to the trash and revokes its direct sharing links.
- **Existing files on a drive:** administrators can copy an individual file from a configured import directory into a selected ShareFile folder. The source stays intact. Import is explicit; storage is reserved for ShareFile and is not synchronized with SMB, FTP or external filesystem changes.

### Search across accessible folders

- **Global filename search:** search all accessible personal and shared folders, including their descendants, regardless of the currently open folder. Matching is case-insensitive, including Cyrillic; special characters are treated literally.
- **Combined filters:** files, folders, images, documents/text, audio, video, archives or other files; date-added range; minimum and maximum file size. A blank filename query allows filtering alone. Type categories use filename extensions; folder sizes are not aggregated.
- **Search results:** up to 100 entries per page, ordered by name and ID, with links to their containing folders. Files can be previewed, downloaded or included in bulk actions directly from the results.
- **Permission-aware results:** deleted and inaccessible nodes are excluded before pagination. Breadcrumbs do not disclose inaccessible parent folders, and each subsequent action checks current permissions again.

### Favorites and recent uploads

- **Personal favorites:** mark a file or folder with the star action in a list or mark the currently open folder. **Избранное** keeps these bookmarks in the server database separately for each account and allows removing them again.
- **Recent files:** **Недавние** lists accessible files across folders, newest first by date added, rather than by last viewing time.
- **Full list actions:** both sections support search/type/date/size filters, pagination, containing-folder navigation, previews, downloads and bulk selection. Deleted or inaccessible entries are hidden, and adding a favorite requires Reader permission.

### Streaming uploads and transfer recovery

- **Bounded-memory transfers:** files are sent in chunks instead of being read entirely into application memory. The server defaults to two upload slots, four download slots and a maximum chunk size of 16 MiB; bundled Web and desktop clients send chunks of at most 4 MiB, respecting smaller server limits.
- **Upload queues:** file selection and drag-and-drop, confirmed progress, pause, resume and cancellation. Empty files and Unicode filenames are supported. A file becomes available for download and sharing only after completion.
- **Durable resume:** the client retrieves the server's committed offset with `HEAD` before continuing. Saved idempotency keys recover a lost upload-creation response without creating another upload. Server recovery reconciles partial and publishing uploads after a process restart.
- **Browser recovery:** queue metadata survives page reloads when localStorage is available, scoped to the current user and origin. Reselect the source file or folder to restore access; the client checks its metadata and sampled SHA-256 fingerprints before resuming. Completed directory entries are skipped.
- **Desktop recovery:** queues survive application restarts. Resume checks the original server/account and the local source's metadata and fingerprints; changed sources are rejected. Sampled fingerprints detect accidental changes and are not a full checksum of a large file.
- **Proxy-compatible uploads:** tus chunks can use `POST` with `X-HTTP-Method-Override: PATCH`, in addition to native `PATCH`. Automatic client retries are bounded; persistent errors leave the queue available for manual continuation.
- **Incomplete-upload retention:** seven days since the last activity by default, configurable in administration from one hour to 365 days. Cancellation and expiration remove incomplete upload data.

### Whole-directory uploads

- **Every client:** upload a directory through the browser, Finder, Windows Explorer or supported Linux file managers. Each directory is one resumable queue item with aggregate progress and one public folder link created after all files finish.
- **Preserved structure:** relative paths, nested directories, duplicate basenames in different folders and empty folders are preserved. A new upload with the same root name gets a separate suffixed folder rather than merging into an earlier upload.
- **Browser behavior:** **Загрузить папку** uses a directory picker; drag-and-drop also preserves empty directories and entirely empty roots. Browsers do not include empty directories in the picker's FileList. Directory drag-and-drop reads every entry batch, including folders with more than 100 direct children.
- **Desktop behavior:** folder pickers and file-manager selections include empty directories. Symbolic links and special files inside a selected tree are rejected.
- **Recovery:** idempotent folder creation and per-file durable offsets resume the existing tree without duplicate folders or retransmitting completed files. Browser cancellation of an unfinished tree sends its created folder to the trash.
- **Sharing:** the browser creates a seven-day link without a password; desktop clients use their current link settings. Manager permission is required because this flow both uploads and creates a public link.
- **Limits:** 10,000 entries and 128 path components per directory. Browser totals must fit within JavaScript's safe integer range.

### Individual downloads and previews

- **Resumable file downloads:** streaming responses support HTTP Range, `HEAD`, ETag and If-Range, including authorized downloads through public links. Permissions and link validity are checked for each request.
- **Preview inside an account:** click a filename in a personal/shared folder or search result. Reader permission is sufficient; no public link is created. Separate download actions remain available.
- **Gallery navigation:** arrow buttons and left/right keys switch between files on the current list page, skipping folders. Esc closes the viewer. Closing or switching files stops media playback.
- **Image previews:** JPEG, PNG, GIF, WebP and BMP, detected from file content.
- **Text previews:** UTF-8 text, limited to the first 256 KiB with a truncation notice. HTML, SVG and XML are escaped and displayed as text. Empty or unsupported files show an explanation and a download action.
- **PDF, audio and video:** the account viewer uses the browser's PDF viewer and native media players. Audio includes MP3, WAV, Ogg/Opus, M4A/MP4, WebM, FLAC and AAC; video includes MP4, WebM and Ogg. HTTP Range enables seeking. Codec support depends on the browser; the server does not transcode media.
- **Public previews:** shared folders provide the image and UTF-8 text previews above. Other formats remain downloadable; the account-only PDF and media viewer is a separate feature.

### Public file and live-folder links

- **Independent links:** create multiple links to one file or folder, each with its own optional password and expiration. The Web dialog offers one day, seven days, 30 days or no expiration; desktop settings also support other durations.
- **Anonymous read access:** recipients need no ShareFile account. A live-folder link shows its current contents, including files added later, with nested navigation, breadcrumbs, previews and separate file downloads.
- **Folder boundaries:** guest navigation and downloads stay inside the shared subtree. Public pages use responsive embedded templates, restrictive security headers and `noindex, nofollow`.
- **Password protection:** a browser unlocks the link with its password before accessing protected contents. Guest authorization lasts at most 24 hours and never beyond the link's expiration.
- **Link management:** **Публичные ссылки** lists the account's created links, creation/expiration dates, password protection and active/expired/revoked status. Links can be revoked individually.
- **Automatic revocation:** deleting or replacing a target, disabling its author's account, or losing the author's Manager permission invalidates affected links. Restoring a file, account or permission does not revive a revoked link.
- **One-time URL delivery:** the server returns the capability URL only when creating a link and stores its token hash. Copy the URL immediately or use the desktop clients' protected history; the server cannot reconstruct a lost URL.
- **Lost creation response:** directory and desktop queues keep the link operation in an uncertain state if it may already have succeeded. They do not automatically create a second link; inspect the existing links or deliberately request a new one.

### Streaming ZIP downloads

- **Entire folder:** **Скачать всё (ZIP)** downloads the currently open folder and all nested/empty directories, regardless of the list's search filter or current page. The archive includes a top-level directory with the folder's name.
- **Selected nodes:** download selected files and folders as one ZIP. Duplicate top-level names get numeric suffixes; selecting a parent and its descendant includes the descendant once.
- **Authenticated and public access:** folder archives require Reader permission. Public archives retain password, expiration, revocation and shared-subtree checks. Permissions are rechecked during streaming.
- **Low disk overhead:** ZIP data is compressed and streamed directly to the browser using one download slot, without a temporary archive or a second copy of file contents on the router.
- **Live contents:** archives are not snapshots. Their length is unknown in advance, and they do not support Range/resume. An interruption, permission loss or read error requires restarting the download.

### Bulk actions in the Web UI

- **Selection:** choose individual files/folders or all entries on the current page, including global search results. The maximum selection is 100 nodes.
- **Move or delete:** move the selection to another folder or send it to the seven-day trash. Editor access is required for every selected node; moving also checks the destination.
- **Collect for sharing:** move selected originals into a new folder, preserve their nested/empty directories, resolve duplicate names with suffixes and open the sharing dialog. The default link lasts seven days, with an optional password. Manager access is required for the destination so the new folder can be shared.
- **Atomic changes:** a missing node, permission failure, name conflict or folder cycle rolls back the entire metadata batch. Node changes and affected sharing-link revocations commit together. A selected parent covers its selected descendants once.
- **Selection lifecycle:** navigation, search/page changes and completed mutations clear the selection. Reader-only selections can still be downloaded as a ZIP.

### Trash and restoration

- **Seven-day trash:** deletion retains files or whole folder trees for seven days before automatic cleanup. Replaced files also enter the trash.
- **Restore:** return an object to its original parent or a selected writable folder. Name conflicts are resolved with numeric suffixes; revoked public links stay revoked.
- **Permanent deletion:** remove a file or an entire trashed tree early, with confirmation in the Web UI. Queue cleanup and crash recovery also reconcile interrupted removal operations.

### Users, groups and inherited permissions

- **Account administration:** create users and administrators, list accounts, enable/disable users and reset passwords. Each user receives a personal root and `Uploads`; the last active administrator cannot be disabled.
- **Groups:** create groups and add/remove members. Folder grants can target a user or a group; the access dialog searches active users and groups.
- **Reader:** browse, search, preview and download accessible files/folders.
- **Editor:** Reader actions plus upload, create folders, rename, move, replace and delete within the allowed space.
- **Manager:** Editor actions plus managing grants and creating public links. Owners manage their personal roots; administrators have full access.
- **Inheritance:** grants apply to descendants, with the strongest role from ownership, direct grants, ancestors and group membership taking effect. Removing a direct grant does not remove another inherited source of access. Moving a node recalculates permissions in its destination.
- **Account changes:** disabling a user revokes their browser sessions, devices and public links while retaining stored files. Password resets revoke sessions and device authorizations.

### Browser sessions and device pairing

- **Local authentication:** username/password login and logout, with Argon2id password hashes. Account and sharing passwords must be 12–256 bytes.
- **Browser sessions:** Secure/HttpOnly/SameSite cookies outside loopback development, Origin checks and CSRF protection for cookie-authenticated mutations. Passwords and session credentials are not stored in localStorage.
- **Request throttling:** login attempts are limited by peer address and account; device requests/polling and public-link password attempts are also limited. Excess requests receive `429` with `Retry-After`.
- **Browser-approved devices:** a desktop client opens a connection request in the browser; the signed-in user explicitly approves it in **Устройства**. Requests last ten minutes; issued device tokens last 180 days and are independently revocable.
- **Device management:** view connected devices, their dates/status and revoke an individual connection. Devices use Bearer tokens with the account's current permissions.
- **Private administration:** administrative routes exist only on the LAN/VPN listener; the public listener returns 404 for them. Configuration management additionally requires an administrator's browser session and rejects device tokens.

### Desktop upload-and-share workflow

- **Separate clients:** SwiftUI/AppKit on macOS, C#/.NET/WPF on Windows, and Python/GTK 3 on Linux. macOS/Linux reuse the Go transfer core; Windows has its own C# protocol/queue core.
- **One flow:** select or drop local files/folders, upload through a resumable queue, create sharing links and copy the completed group's links together, one per line. A directory produces one link for its entire tree.
- **Defaults and settings:** personal `Uploads`, seven-day links and no password. Choose another accessible Manager folder, expiration or password in client settings; open the Web UI for broader file and permission management.
- **Background integration:** Finder Service, Explorer **Send to**, and Nautilus/Nemo/Dolphin actions hand selections to the client. Background selections do not need an open transfer window; menu/tray actions reopen the queue and settings.
- **Batch completion:** one clipboard update and one completion notification after the selected group finishes. Clipboard failure is reported. Notifications can be disabled without disabling link copying; macOS requests notification permission when needed.
- **Queue controls:** pause, continue, cancel/remove a transfer, copy a completed link and deliberately create a new link. Removing a completed queue entry does not delete its server file or its saved history.
- **Protected credentials:** device tokens, link passwords and saved capability URLs use macOS Keychain, Windows Credential Manager or Linux Secret Service. Queue checkpoints contain no credentials or public URLs.
- **Connection safety:** pairing validates the expected server origin, clients verify TLS through the platform trust store, and authenticated requests do not forward credentials through redirects to another origin.
- **Installation and background lifecycle:** Windows and Linux provide optional login autostart, single-instance handoff and per-user uninstallers. Closing their window keeps the client available in the background; quitting pauses transfers. Disconnecting removes the local device token, while revoking server authorization is a separate Web UI action.

### Sent-link history on all desktop platforms

- **Persistent history:** retain up to 200 links independently of the upload queue, across restarts and disconnection. The history screen shows the current server/account and supports filename/folder-name search, creation/expiration dates and revocation status.
- **Recent links:** menu/tray actions expose the eight most recent entries for copying, opening or revoking.
- **Offline access:** saved active links can be copied or opened without a client connection. Revocation requires the original server and account; confirmed revocation removes the stored capability and marks the history entry revoked.
- **Secret-free index:** local history metadata contains names, dates and IDs; URLs live in the platform secret store. Evicting an old history entry removes its local secret, without revoking the server link.
- **Save failures:** if persistent history cannot be written, the created link remains in the current queue with a warning so it can still be copied.

### Clipboard screenshots and image uploads

- **Global shortcut:** with the client running and connected, press **⌘⇧U** on macOS or **Ctrl+Alt+U** on Windows/Linux to upload an image from the clipboard. The window and menu/tray also provide **Из буфера**.
- **No manual image file:** the client converts the clipboard image to PNG, uses the current destination/link settings, uploads it through the resumable queue and copies the sharing link on completion. Clipboard reading happens only after the user's action.
- **Limits and errors:** at most 25 megapixels and 32 MiB. Empty, non-image or inaccessible clipboard contents produce an error; an unavailable shortcut still allows button/menu actions.
- **Private spool:** generated PNGs survive pause, failure and restart, and are removed after completion or confirmed cancellation. Cleanup is confined to the client's clipboard directory.
- **Linux desktops:** X11 uses Keybinder 3 for the global shortcut. Wayland uses the GlobalShortcuts portal, which may ask for approval or offer another combination; ShareFile opens its window for clipboard access and needs focus. Desktop keyboard settings can alternatively bind `sharefile --clipboard`.

### Server configuration and storage migration

- **Administration → Settings:** edit all JSON configuration fields: storage disk/directory and identity, reserve space, upload/download slots, chunk size, listener addresses and public/admin URLs, TLS certificate/key paths, Web UI path, logging, import directories and local development mode.
- **Disk selection:** list mounted volumes with mount path, device, filesystem and free space. Refresh after connecting a drive; selection suggests `<mount>/sharefile`, and the absolute path remains editable.
- **Persisted drafts:** **Сохранить черновик** validates and saves `<config>.pending` with permissions `600`; drafts survive restarts. Delete a draft or apply it explicitly with **Применить и перезапустить**. Revision checks detect concurrent edits and manual changes to the active JSON.
- **Apply/reload:** the server stops accepting requests, drains active requests for up to 30 seconds, closes SQLite and restarts its listeners inside the same process. The UI reconnects at the same origin or provides the new administration URL.
- **Storage migration:** changing the data directory copies files, SQLite metadata, accounts, links, sessions and partial uploads to an absent or empty destination. Streaming copy checks free space and the configured reserve; original storage is preserved. The server is unavailable during migration.
- **Failure recovery:** validation, copy or startup failure retains/restores the previous configuration and reports the reason. A completed destination copy may remain after a later apply failure; it is not automatically reused.
- **Immediate retention setting:** unfinished-upload lifetime is stored separately and applies without a configuration reload.

### Storage safety, maintenance and deployment

- **Dedicated storage:** a volume marker and process lock protect the selected data directory. Missing, replaced or incorrectly marked storage causes requests to fail; the server does not create a fallback data directory on another filesystem.
- **Free-space reserve:** disk capacity and a configurable reserve constrain uploads and migration. There is no per-user quota or application-level individual file-size limit; browser sizes must fit in `Number.MAX_SAFE_INTEGER`, and the API supports int64 byte sizes.
- **Filesystem boundaries:** storage/import paths are confined to their roots, with checks against traversal and unsuitable source files. SQLite holds metadata separately from file contents; startup recovery reconciles interrupted operations.
- **Web delivery:** a Russian React interface with responsive navigation, role-aware actions, transfer progress and loading/empty/error states is served as compiled assets by the Go backend. No Node.js, extra database server, CDN, external fonts or analytics are needed on the router. Preview endpoints escape active text formats and only serve recognized passive formats inline.
- **Maintenance CLI:** initialize storage, create/reset an administrator and perform offline backup/restore. Backup requires exclusive storage access and writes outside the storage directory. Restore requires an absent destination and validates archive paths, storage identity, SQLite integrity and foreign keys. These operations and procedures are documented in [docs/deployment.md](docs/deployment.md).
- **Entware service:** automatic startup, supervised process restart, graceful shutdown, bounded JSON logs, `/healthz` process liveness and `/readyz` storage/database readiness.
- **Router packages:** a static Linux ARM64 backend, compiled Web UI, configuration templates, installer/updater, supervisor and LAN TLS tools. Installation verifies SHA-256; updates preserve configuration, certificates, accounts and data and restore previous application files if startup fails.
- **LAN HTTPS:** installation creates a local CA and server certificate for direct LAN/VPN access. The TLS helper renews a near-expiry server certificate when rerun; renewal is not scheduled automatically.
- **Independent desktop packages:** universal macOS, Windows x64/ARM64 and Linux x86_64/ARM64 builds, per-user installation, platform integrations and parallel build/check jobs in one workflow. Native platform and external KeenDNS acceptance checks remain separately documented.

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

The single [Build and release workflow](.github/workflows/build.yml) runs on pushes to `master`/`main`, pull requests, manual dispatch, and publication of a GitHub Release. After a preparation job, four independent jobs build and check the router with Web UI, macOS universal, Windows x64/ARM64, and Linux x86_64/ARM64 in parallel. An additional parallel Linux job checks the Windows protocol against a real Go backend. Each platform job runs its own build, validation and packaging steps and uploads a separate Actions artifact: `sharefile-router`, `ShareFile-macOS`, `ShareFile-Windows`, or `ShareFile-Linux`.

A pushed `v*` tag publishes a GitHub Release automatically. Publishing a release through **Releases → Draft a new release → Publish release** also attaches the packages automatically. In both cases, the preparation job looks for a successful run of this workflow for the exact release commit, with all four unexpired package artifacts. When found, the build jobs are skipped and those existing packages are downloaded, their SHA-256 files checked, and the files attached to the release. Otherwise, all five build/check jobs run and must succeed before their new packages are attached. Pull-request runs are excluded from package reuse.

The release includes these router assets plus the macOS ZIP, both Windows ZIPs, both Linux tarballs and their SHA-256 files:

- `sharefile-router.tar.gz`: server, Web UI, configuration templates, and Entware scripts.
- `sharefile-router.tar.gz.sha256`: archive checksum.
- `install-sharefile.sh`: the standalone download/install/update script.

Commit and push the source, installer, and workflow changes first. Then choose an unused version tag, for example:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Wait for **Actions → Build and release** to finish, then check the repository's **Releases** page. Tags containing a hyphen, such as `v0.2.0-rc.1`, are published as prereleases and are available through `--version`; the default installer selects the latest stable release.

Publishing uses the repository's `GITHUB_TOKEN` with `contents: write` only in the release job and `actions: read` for package lookup/download. No personal access token is needed for the workflow. Commit and push the updated workflow before tagging that commit or publishing its release. GitHub release downloads in the installation command assume the repository/releases are public. See the [GitHub CLI release documentation](https://cli.github.com/manual/gh_release_create).

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

## API

The full API contract is [api/openapi.json](api/openapi.json). Authenticate with `POST /api/v1/auth/login`; cookie-authenticated mutations require `X-CSRF-Token`. Devices use `Authorization: Bearer <device-token>`. Administrative routes exist only on the private listener; the public listener returns 404 for them.

Uploads use `POST /api/v1/uploads` to create a session, `HEAD` to retrieve the durable offset, and sequential `PATCH` chunks. The backend also accepts `POST` with `X-HTTP-Method-Override: PATCH`. Downloads support Range, ETag, If-Range, and HEAD. Share URLs are returned once; persist them on the client.

- **Files and search:** `/api/v1/folders`, `/api/v1/nodes/{id}`, `/api/v1/search`; favorites/recent collections use `view=favorites` or `view=recent` on search, and bookmarks use `PUT/DELETE /api/v1/nodes/{id}/favorite`. Content is at `/api/v1/nodes/{id}/content`, preview metadata/content at `/api/v1/nodes/{id}/preview` and `/api/v1/nodes/{id}/preview/content`.
- **Batch and ZIP:** `POST /api/v1/nodes/batch` with `move`, `delete` or `collect`; `GET/HEAD /api/v1/nodes/archive?id=...` for a selection and `/api/v1/nodes/{id}/archive` for a complete folder.
- **Access and trash:** `/api/v1/nodes/{id}/grants`, `/api/v1/nodes/{id}/principals`, `/api/v1/trash` and `/api/v1/trash/{id}/restore`.
- **Links and devices:** `/api/v1/shares`, `/api/v1/device-requests`, `/api/v1/devices`; anonymous pages/downloads at `/s/{token}` and its nested node, preview, archive and unlock routes.
- **Private administration:** `/api/v1/admin/users`, `/api/v1/admin/groups`, `/api/v1/admin/import`, `/api/v1/admin/settings`, `/api/v1/admin/config`, `/api/v1/admin/config/apply` and `/api/v1/admin/volumes`.

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

Self-contained x64 and ARM64 packages include the runtime. Extract the entire ZIP and run `Install.cmd`; installation is per-user and does not need administrator rights. Windows packages are currently unsigned. On Windows, build with `clients/windows/build.ps1`; the **Windows (x64 and ARM64)** job in **Build and release** produces the **ShareFile-Windows** Actions artifact.

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
