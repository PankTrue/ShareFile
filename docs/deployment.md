# Установка ShareFile через Entware

Инструкция включает Go backend и статический React Web UI. На реальном KN-1811 установка ещё не выполнялась. Адрес `192.168.1.1`, домен и пути ниже — примеры; их заменяют после проверки конкретной установки. Desktop-пакеты выпускаются отдельно.

Для установки опубликованного GitHub Release одним скриптом использовать [англоязычный README](../README.md#install-on-the-router-from-github). Этот установщик проверяет SHA-256 пакета и автоматически выбирает первую установку или обновление существующего приложения. Обычное обновление сохраняет конфигурацию, сертификаты и данные; ниже описаны локальная сборка и ручная настройка.

## Первая установка и доступ из LAN/VPN

Подтверждённые параметры: архитектура `aarch64`, LAN-адрес `192.168.1.1`, Entware `/opt` на `/dev/sdb` (57,6 ГБ), отдельный ext4-накопитель `/dev/sda` в `/tmp/mnt/745ee3e5-6027-4b29-8e14-3fdc29729706`. Данные и SQLite находятся в каталоге `sharefile` второго накопителя. Его `noexec` допускает хранение данных; бинарник, React-статика и логи находятся в `/opt`.

Начальная конфигурация `sharefile.lan.example.json` предоставляет пользовательский интерфейс, ссылки и админку по одному HTTPS-origin `https://192.168.1.1:8081`. Они обслуживаются приватным listener; административные операции по-прежнему требуют прав администратора. HTTP-listener `127.0.0.1:8080` остаётся внутри роутера. В LAN/VPN SSH-туннель не нужен. Для VPN требуется маршрут к `192.168.1.1`; публикация через KeenDNS в этот вариант не входит.

Исполняемый `/opt/etc/init.d/S90sharefile` запускается механизмом Entware `rc.unslung` после подключения накопителя с `/opt`. Supervisor перезапускает backend после выхода, включая ошибки из-за ещё не подключённого HDD или не назначенного LAN IP. Реальные boot/hotplug и правила доступа VPN нужно проверить на роутере. [Исходник rc.unslung](https://github.com/Entware/rtndev/blob/master/entware-opt/files/rc.unslung).

На Mac из корня репозитория:

```sh
cd /Users/pank/Code/PetProjects/ShareFile
nvm use 24.21.0
make router-release

ROUTER_HOST=192.168.1.1
ROUTER_SSH_PORT=222
ssh -p "$ROUTER_SSH_PORT" "root@$ROUTER_HOST" \
  'mkdir -p /opt/tmp && mkdir /opt/tmp/sharefile-lan-release && tar -xzf - -C /opt/tmp/sharefile-lan-release' \
  < backend/bin/sharefile-router.tar.gz
```

Если приложение уже установлено по прежней инструкции, включить LAN-доступ и автозапуск:

```sh
ssh -t -p "$ROUTER_SSH_PORT" "root@$ROUTER_HOST" \
  'sh /opt/tmp/sharefile-lan-release/enable-lan.sh'
```

`enable-lan.sh` создаёт резервную копию текущего JSON, сохраняет `data_dir`, `volume_id`, настройки передачи и импорта, обновляет только сеть/TLS и сервисные скрипты, затем перезапускает сервер. Данные и аккаунты не инициализируются повторно. Пакеты `openssl-util`, `jq`, `daemonize` и `daemontools-encore` устанавливаются через opkg.

Для первой установки вместо `enable-lan.sh` выполнить:

```sh
ssh -t -p "$ROUTER_SSH_PORT" "root@$ROUTER_HOST" \
  'sh /opt/tmp/sharefile-lan-release/first-install.sh'
```

Первичный установщик проверяет mount накопителей и отсутствие прежней установки до изменений, создаёт storage и администратора, TLS, регистрирует автозапуск и запускает сервис. Ввести и подтвердить пароль `admin`, минимум 12 символов. Прерванная установка может оставить скопированные файлы; повторный запуск их не перезаписывает. Сохранить конфигурацию/`volume_id`, устранить причину и продолжить оставшиеся шаги вручную.

TLS создаётся на роутере: `/opt/etc/sharefile/lan-ca.pem` — публичный корневой сертификат, `lan-ca-key.pem` — закрытый ключ CA, `admin-cert.pem` и `admin-key.pem` — сертификат/ключ сервера с SAN `IP:192.168.1.1`. Закрытые ключи имеют права 600 и не входят в release-архив. CA действует 10 лет, серверный сертификат — год. Повторный запуск настройки сохраняет CA и обновляет серверный сертификат при истечении, смене IP или остатке срока менее 30 дней; периодическое продление автоматически не запланировано. [Создание сертификатов OpenSSL](https://docs.openssl.org/3.6/man1/openssl-req/).

На каждом клиенте добавить только публичный `lan-ca.pem` в доверенные. На Mac получить его через SSH и добавить доверие SSL для текущего пользователя:

```sh
ssh -p "$ROUTER_SSH_PORT" "root@$ROUTER_HOST" \
  'cat /opt/etc/sharefile/lan-ca.pem' > /tmp/sharefile-lan-ca.pem

security add-trusted-cert -r trustRoot -p ssl \
  -k "$HOME/Library/Keychains/login.keychain-db" /tmp/sharefile-lan-ca.pem

curl --fail --cacert /tmp/sharefile-lan-ca.pem https://192.168.1.1:8081/readyz
```

macOS запросит подтверждение изменения доверия. Через UI: **Keychain Access**, импорт сертификата в **login**, открыть сертификат → **Trust** → **Secure Sockets Layer (SSL)** → **Always Trust**. [Настройки доверия Apple](https://support.apple.com/guide/keychain-access/change-the-trust-settings-of-a-certificate-kyca11871/mac).

Открыть `https://192.168.1.1:8081`; для первой установки вход — `admin` и созданный пароль. Сервис работает в фоне после закрытия SSH. Проверить `readyz`, вход и загрузку файла, затем автозапуск после плановой перезагрузки роутера. Команды start/stop/restart/check приведены в разделе 5. Для дальнейшей публикации через KeenDNS использовать отдельную production-конфигурацию из раздела 3, сохранив текущие `data_dir` и `volume_id`.

## 1. Проверить устройство и накопитель

В shell Entware сначала выполнить только чтение:

```sh
uname -m
uname -r
cat /proc/mounts
df -h /opt
cat /proc/meminfo
cat /opt/etc/opkg.conf
```

Ожидается ARM64 (`aarch64`). Проверить точную версию KeeneticOS в панели роутера, фактическую файловую систему и mount HDD, публичный WAN IP/режим KeenDNS, свободное место, публикации SMB/FTP и свободные порты. Go 1.26 требует ядро Linux 3.2+; фактическое ядро следует сопоставить с [Go minimum requirements](https://go.dev/wiki/MinimumRequirements).

Entware и бинарник находятся на `/opt`; данные и SQLite — в `/tmp/mnt/745ee3e5-6027-4b29-8e14-3fdc29729706/sharefile` на втором накопителе. Подтвердить его mount и свободное место через `cat /proc/mounts` и `df -h /tmp/mnt/745ee3e5-6027-4b29-8e14-3fdc29729706`. Рекомендуется ext4; FAT32 не подходит для файлов больше 4 ГБ. Форматирование и перенос существующих данных инструкция не выполняет. Выделенная директория ShareFile должна быть закрыта для записи сторонним приложениям.

## 2. Собрать вне роутера

На машине разработчика, из корня репозитория:

```sh
make check
make router-release
make web-check
```

Готовый пакет: `backend/bin/sharefile-router.tar.gz`, содержит статическую Linux ARM64-сборку без cgo, статику в `dist/`, LAN/SSH/KeenDNS-конфигурации и скрипты Entware. Загрузить и распаковать его в staging-каталог `/opt/tmp/sharefile-lan-release`, как в командах первой установки выше. Для установки сразу с KeenDNS выполнить следующие разделы вручную вместо `first-install.sh`.

## Настройки установленного сервера через Web UI

После обновления backend и Web UI открыть приватный HTTPS-вход → «Настройки». Все поля текущего JSON-конфига доступны для редактирования. Выбрать подключённый диск или задать абсолютную директорию, сохранить черновик и нажать «Применить и перезапустить». Начальная установка по-прежнему требует существующего storage и администратора.

Перенос требует пустого целевого каталога и достаточного места для всех файлов, SQLite, частичных загрузок и заданного резерва. Сервер копирует данные после завершения HTTP-запросов и закрытия SQLite. Исходную копию оставляет; автоматически объединять два существующих хранилища нельзя. Во время операции сервер недоступен. После переноса проверить вход, файлы и `/readyz` на действующем адресе, прежде чем отключать старый диск. Черновики сохраняются рядом с `/opt/etc/sharefile.json` как `.pending`; обычный service restart их не применяет.

Сетевые адреса и сертификаты должны соответствовать настройкам LAN/VPN/KeenDNS: Web UI проверяет конфигурацию и локальные файлы, но не настраивает роутер, DNS и выпуск сертификатов. При смене административного URL перейти по показанному новому адресу. Подробнее — [web-ui.md](web-ui.md#настройки-сервера).

## 3. Подготовить конфигурацию и TLS

Скопировать `sharefile.example.json` в `/opt/etc/sharefile.json` и настроить:

- `data_dir` — отдельная директория на HDD, существующий родитель.
- `volume_id` — уникальная метка этого хранилища, до 128 байт. Её сохраняют вместе с конфигурацией; backup/restore должны использовать ту же метку.
- `public_url` — реальный HTTPS KeenDNS origin с портом, если он нестандартный. `public_addr` — loopback endpoint для TLS-терминирующего роутера, по умолчанию `127.0.0.1:8080`.
- `admin_addr` — конкретный LAN/VPN IP роутера и свободный порт, `admin_url` — соответствующий HTTPS origin. Wildcard/WAN listeners конфигурация отклоняет. В примере приватный порт `8081`.
- `admin_cert`/`admin_key` — отдельный TLS-сертификат и ключ для приватного адреса. Сертификат должен быть доверенным на клиентской машине и содержать адрес/hostname в SAN; получение и установка доверия зависят от вашей локальной PKI.
- `log_file` — путь JSON-лога, в примере на накопителе Entware `/opt`. Сервер хранит текущий файл и `.1`, каждый до 1 МиБ. Без параметра пишет в stderr.
- `web_dir` — абсолютный путь к сборке React, в примере `/opt/share/sharefile/web`. На роутер загружается только `web/dist/`, без исходников и `node_modules`.
- `import_dirs` — только разрешённые локальные источники импорта. Пустой массив полностью отключает импорт.

Публичное HTTPS завершается на Keenetic, поэтому backend public listener использует HTTP внутри роутера. Если KeenDNS не достигает loopback на конкретной версии OS, проверить привязку к конкретному LAN IP и ограничение доступа firewall; WAN-доступ непосредственно к HTTP backend не нужен. Приватный listener обслуживает TLS самим Go-сервером. Публикация приватного listener через KeenDNS не предусматривается.

## 4. Установить бинарник и supervisor

На роутере используются `daemonize` и `daemontools-encore`: первый запускает supervisor в фоне, второй перезапускает ShareFile при завершении процесса. Они присутствуют в [ARM64-репозитории Entware](https://bin.entware.net/aarch64-k3.10/Packages.html). Семантика команд: [daemonize](https://software.clapper.org/daemonize/daemonize.html), [supervise](https://untroubled.org/daemontools-encore/supervise.8.html), [svc](https://untroubled.org/daemontools-encore/svc.8.html).

Команды выполняются в shell Entware после подтверждения USB mount и подготовленной конфигурации. Для существующей установки сначала выполнить stop из следующего раздела; не перезаписывать работающий бинарник.

```sh
opkg update
opkg install daemonize daemontools-encore
mkdir -p /opt/sbin /opt/etc/init.d /opt/etc/sharefile-service /opt/etc/sharefile /opt/var/log/sharefile /opt/share/sharefile/web
chmod 700 /opt/etc/sharefile /opt/var/log/sharefile
cp /opt/tmp/sharefile-lan-release/sharefile-linux-arm64 /opt/sbin/sharefile
cp -R /opt/tmp/sharefile-lan-release/dist/. /opt/share/sharefile/web/
cp /opt/tmp/sharefile-lan-release/service/run /opt/etc/sharefile-service/run
cp /opt/tmp/sharefile-lan-release/S90sharefile /opt/etc/init.d/S90sharefile
chmod 755 /opt/sbin/sharefile /opt/etc/sharefile-service/run /opt/etc/init.d/S90sharefile
chmod 600 /opt/etc/sharefile.json /opt/etc/sharefile/admin-key.pem
/opt/sbin/sharefile init-storage -config /opt/etc/sharefile.json
/opt/sbin/sharefile admin-create -config /opt/etc/sharefile.json -username admin
```

Скопировать TLS-файлы в указанные пути до запуска сервера. `init-storage` только создаёт собственный каталог/маркер; повтор с той же меткой допустим, с другой — ошибка. После успешного старта доступна SQLite schema v3; существующие версии 1/2 автоматически мигрируют с сохранением данных. Существующий PostgreSQL-черновик не переносится автоматически.

Сервисный `run` задаёт `GOMEMLIMIT=96MiB` и `GOGC=50` как начальные настройки сборщика мусора. Это мягкий бюджет Go heap, не лимит RSS. Значения и влияние на маршрутизацию нужно измерить на устройстве. Argon2id использует 32 МиБ на hash, такие операции сериализованы.

## 5. Запуск и остановка

```sh
/opt/etc/init.d/S90sharefile start
/opt/etc/init.d/S90sharefile check
/opt/etc/init.d/S90sharefile stop
/opt/etc/init.d/S90sharefile restart
```

`start` поднимает supervisor только для этой сервисной директории. `stop` сохраняет `down` и отправляет SIGTERM серверу, ожидает остановки до 39 секунд и не вызывает killall. Сам supervisor остаётся запущенным и ждёт `start`. Остановка действует до следующего start, в том числе при загрузке Entware. Для отключения автозапуска убрать executable bit у init-скрипта: `chmod 644 /opt/etc/init.d/S90sharefile`; для включения вернуть `chmod 755` и выполнить start.

Supervisor перезапускает процесс после падения; при отсутствующем/чужом маркере startup завершается ошибкой. При отключении накопителя работа не продолжается в fallback-каталоге. После возвращения накопителя сервис повторяет запуск и восстанавливает журналы/offset. Проверить реальный hotplug и перезагрузку роутера — поведение Entware mount и autorun ещё не испытано.

Liveness: `/healthz`; готовность HDD/SQLite: `/readyz`. Проверка backend до публикации:

```sh
curl --fail http://127.0.0.1:8080/readyz
```

Перед CLI-обслуживанием сервис должен быть остановлен. `process.lock` в data_dir отклоняет второй процесс, включая backup и аварийный admin-reset.

## 6. Публикация через KeenDNS

Для публичного origin создать отдельную публикацию приложения на роутере: `files.<ваш-домен>`, public listener `8080`. Включить HTTPS. Публикация должна разрешать доступ гостя к `/s/...`; приложение самостоятельно проверяет сессии для приватных пользовательских операций. Не включать дополнительную KeenDNS-аутентификацию перед публичными ссылками. Настройки публикации и допустимые Cloud порты описаны в [документации Keenetic](https://support.keenetic.com/carrier/kn-1713/en/15884-remote-access-to-home-network-resources-with-keendns.html).

При публичном WAN IP использовать согласованный Direct-сценарий; без него — Cloud. Настоящие PATCH/HEAD/Range, headers и длинные передачи проверяются через внешний HTTPS origin. Если Cloud блокирует PATCH, поддерживается `POST` с `X-HTTP-Method-Override: PATCH`; работоспособность обоих способов на вашем KeenDNS ещё не подтверждена. Backend не обещает отсутствие внешних лимитов proxy/файловой системы.

Приватный `8081` доступен только по LAN/VPN: не создавать для него KeenDNS-публикацию, NAT forwarding или WAN firewall allow. Из внешней сети `https://files.<домен>/api/v1/admin/users` должен возвращать 404; подключение к приватному порту должно быть недоступно.

React UI доступен на публичном origin для пользовательских операций; админка — на приватном HTTPS origin. Указать абсолютный `web_dir` и перезапустить backend. Директорию статики ограничить своей сборкой; секретные конфигурации туда не помещать. Статическая сборка общая, административные API и меню доступны только через приватный listener. При обновлении заменять всю директорию сборки вместе с новым `index.html` и hashed assets.

## 7. Offline backup и восстановление

Остановить сервис. Убедиться по `check`, что процесс down. Создать новый архив вне data_dir, предпочтительно на другом накопителе; свободное место должно вместить готовые файлы, корзину и незавершённые байты.

```sh
/opt/etc/init.d/S90sharefile stop
/opt/etc/init.d/S90sharefile check
/opt/sbin/sharefile backup -config /opt/etc/sharefile.json -archive /ABSOLUTE/BACKUP/sharefile-2026-10-04.tar
/opt/etc/init.d/S90sharefile start
```

`backup` удерживает process lock, завершает recovery, checkpoint SQLite WAL и закрывает БД до чтения. Архив не сжимается, копируется потоково, содержит маркер, согласованную БД и физические объекты/uploads. Существующий архив не перезаписывается; неполный архив при ошибке удаляется. TLS/config сохраняются отдельно, защищённо; архив содержит парольные hashes и hashes bearer-токенов и также требует ограничения доступа.

Для восстановления сделать отдельную конфигурацию с **той же** `volume_id`, но новым ещё не существующим `data_dir` под проверенным USB parent. Все остальные обязательные настройки остаются валидными. Не запускать init-storage для нового назначения перед restore.

```sh
/opt/sbin/sharefile restore -config /opt/etc/sharefile-restore.json -archive /ABSOLUTE/BACKUP/sharefile-2026-10-04.tar
```

Restore отклоняет traversal, symlinks и повторяющиеся записи, проверяет маркер, SQLite integrity/foreign keys, восстанавливает незавершённые операции и сверяет наличие/размер файлов. Полное содержимое архива не имеет отдельного криптографического manifest; контрольную сумму самого tar стоит хранить отдельно. При ошибке новый каталог сохраняется для диагностики, не подключать его как live storage.

Затем проверить файлы/права в восстановленном хранилище отдельным запуском с restore-конфигурацией. Переключение production выполнить после остановки текущего сервиса: перенести проверенные значения в `/opt/etc/sharefile.json`, сохранить старый data_dir и запустить сервис. CLI не удаляет и не перезаписывает старый каталог.

Для аварийного восстановления администратора:

```sh
/opt/etc/init.d/S90sharefile stop
/opt/sbin/sharefile admin-reset -config /opt/etc/sharefile.json -username admin
/opt/etc/init.d/S90sharefile start
```

## 8. Обновление, откат и приёмка

Перед обновлением — offline backup, stop, сохранение прежнего бинарника и конфигурации. Скопировать новый бинарник через отдельный staging-файл и rename, запустить, проверить readyz и пользовательский сценарий. Возврат бинарника возможен только если он поддерживает текущую версию схемы. После миграции на schema v3 бинарник, поддерживающий максимум v1/v2, не откроет базу: для такого отката нужен согласованный backup до обновления, восстановленный в новую директорию. Старые файлы не удалять до успешной проверки.

Осталось проверить на KN-1811: реальный файл больше 4 ГБ с совпадением SHA-256; обрыв/возобновление через внешний KeenDNS; Range; crash/restart; USB отключение/возврат; boot/autostart; роли и отзыв ссылок; недоступность admin из WAN; backup/restore; RSS/CPU и влияние на маршрутизацию. Локальные проверки перечислены в [validation.md](validation.md).
