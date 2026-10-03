# Установка ShareFile через Entware

Инструкция подготовлена для backend-этапа. На реальном KN-1811 установка ещё не выполнялась. Адрес `192.168.1.1`, домен и пути ниже — примеры; их заменяют после проверки конкретной установки. Полный Web UI и desktop-пакеты выпускаются отдельно.

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

В примере Entware `/opt` и `/opt/var/lib/sharefile` находятся на USB HDD. Подтвердить это по mount/df, иначе выбрать абсолютный путь на проверенном USB mount. Рекомендуется ext4; FAT32 не подходит для файлов больше 4 ГБ. Форматирование и перенос существующих данных инструкция не выполняет. Выделенная директория ShareFile должна быть закрыта для записи сторонним приложениям.

## 2. Собрать вне роутера

На машине разработчика, из корня репозитория:

```sh
make check
make arm64
```

Готовый файл: `backend/bin/sharefile-linux-arm64`, статическая Linux ARM64-сборка без cgo. Загрузить его и файлы `deploy/entware/` в staging-каталог на HDD. Ниже staging-каталог обозначен `/opt/tmp/sharefile-release`; создать и заполнить его отдельно перед командами установки.

## 3. Подготовить конфигурацию и TLS

Скопировать `sharefile.example.json` в `/opt/etc/sharefile.json` и настроить:

- `data_dir` — отдельная директория на HDD, существующий родитель.
- `volume_id` — уникальная метка этого хранилища, до 128 байт. Её сохраняют вместе с конфигурацией; backup/restore должны использовать ту же метку.
- `public_url` — реальный HTTPS KeenDNS origin с портом, если он нестандартный. `public_addr` — loopback endpoint для TLS-терминирующего роутера, по умолчанию `127.0.0.1:8080`.
- `admin_addr` — конкретный LAN/VPN IP роутера и свободный порт, `admin_url` — соответствующий HTTPS origin. Wildcard/WAN listeners конфигурация отклоняет. В примере приватный порт `8081`.
- `admin_cert`/`admin_key` — отдельный TLS-сертификат и ключ для приватного адреса. Сертификат должен быть доверенным на клиентской машине и содержать адрес/hostname в SAN; получение и установка доверия зависят от вашей локальной PKI.
- `log_file` — каталог на HDD для JSON-логов. Сервер хранит текущий файл и `.1`, каждый до 1 МиБ. Без параметра пишет в stderr.
- `import_dirs` — только разрешённые локальные источники импорта. Пустой массив полностью отключает импорт.

Публичное HTTPS завершается на Keenetic, поэтому backend public listener использует HTTP внутри роутера. Если KeenDNS не достигает loopback на конкретной версии OS, проверить привязку к конкретному LAN IP и ограничение доступа firewall; WAN-доступ непосредственно к HTTP backend не нужен. Приватный listener обслуживает TLS самим Go-сервером. Публикация приватного listener через KeenDNS не предусматривается.

## 4. Установить бинарник и supervisor

На роутере используются `daemonize` и `daemontools-encore`: первый запускает supervisor в фоне, второй перезапускает ShareFile при завершении процесса. Они присутствуют в [ARM64-репозитории Entware](https://bin.entware.net/aarch64-k3.10/Packages.html). Семантика команд: [daemonize](https://software.clapper.org/daemonize/daemonize.html), [supervise](https://untroubled.org/daemontools-encore/supervise.8.html), [svc](https://untroubled.org/daemontools-encore/svc.8.html).

Команды выполняются в shell Entware после подтверждения USB mount и подготовленной конфигурации. Для существующей установки сначала выполнить stop из следующего раздела; не перезаписывать работающий бинарник.

```sh
opkg update
opkg install daemonize daemontools-encore
mkdir -p /opt/etc/sharefile-service /opt/etc/sharefile /opt/var/lib /opt/var/log/sharefile
chmod 700 /opt/etc/sharefile /opt/var/log/sharefile
cp /opt/tmp/sharefile-release/sharefile-linux-arm64 /opt/sbin/sharefile
cp /opt/tmp/sharefile-release/service/run /opt/etc/sharefile-service/run
cp /opt/tmp/sharefile-release/S90sharefile /opt/etc/init.d/S90sharefile
chmod 755 /opt/sbin/sharefile /opt/etc/sharefile-service/run /opt/etc/init.d/S90sharefile
chmod 600 /opt/etc/sharefile.json /opt/etc/sharefile/admin-key.pem
/opt/sbin/sharefile init-storage -config /opt/etc/sharefile.json
/opt/sbin/sharefile admin-create -config /opt/etc/sharefile.json -username admin
```

Скопировать TLS-файлы в указанные пути до запуска сервера. `init-storage` только создаёт собственный каталог/маркер; повтор с той же меткой допустим, с другой — ошибка. После успешного старта доступна SQLite schema v1. Существующий PostgreSQL-черновик не переносится автоматически.

Сервисный `run` задаёт `GOMEMLIMIT=96MiB` и `GOGC=50` как начальные настройки сборщика мусора. Это мягкий бюджет Go heap, не лимит RSS. Значения и влияние на маршрутизацию нужно измерить на устройстве. Argon2id использует 32 МиБ на hash, такие операции сериализованы.

## 5. Запуск и остановка

```sh
/opt/etc/init.d/S90sharefile start
/opt/etc/init.d/S90sharefile check
/opt/etc/init.d/S90sharefile stop
/opt/etc/init.d/S90sharefile restart
```

`start` поднимает supervisor только для этой сервисной директории. `stop` сохраняет `down` и отправляет SIGTERM серверу, ожидает остановки до 39 секунд и не вызывает killall. Сам supervisor остаётся запущенным и ждёт `start`. Такой stop сохраняется при следующем boot до явного start; для отключения автозапуска навсегда также убрать executable bit у init-скрипта.

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

Полный Web UI ещё не выпущен. Установив его отдельную сборку, указать абсолютный `web_dir` и перезапустить backend. Директорию статики ограничить своей сборкой; секретные конфигурации туда не помещать.

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

Перед обновлением — offline backup, stop, сохранение прежнего бинарника и конфигурации. Скопировать новый бинарник через отдельный staging-файл и rename, запустить, проверить readyz и пользовательский сценарий. Для той же schema v1 возможен возврат бинарника; миграции будущих версий могут требовать восстановления согласованного backup в новую директорию. Старые файлы не удалять до успешной проверки.

Осталось проверить на KN-1811: реальный файл больше 4 ГБ с совпадением SHA-256; обрыв/возобновление через внешний KeenDNS; Range; crash/restart; USB отключение/возврат; boot/autostart; роли и отзыв ссылок; недоступность admin из WAN; backup/restore; RSS/CPU и влияние на маршрутизацию. Локальные проверки перечислены в [validation.md](validation.md).
