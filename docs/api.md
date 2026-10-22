{% raw %}

# Спецификация API IGoNotes

Документ описывает REST-интерфейс поставляемого приложения по обработчикам `internal/handlers`, моделям `internal/model` и Git-сервисам `internal/service` и `internal/git`.

## Базовый URL

```
http://127.0.0.1:8080
```

Все пути ниже абсолютны относительно этого URL и начинаются с `/api`. JSON-запросы передаются с `Content-Type: application/json`; загрузка файла использует `multipart/form-data`.

Порт настраивается при запуске: `igonotes --port 9000` использует `http://127.0.0.1:9000`. Для будущего расширения нужно указать фактический loopback URL и порт этой же установки; это не удалённый API.

### Локальный доступ и первичная настройка

Обычные API и management Clipper защищены `RequireLocalOrigin`: `Host` должен быть локальным (`localhost` или loopback IP). Если передан `Origin`, его схема, нормализованный хост и порт должны совпадать с запросом; `localhost` и `127.0.0.1` не взаимозаменяемы. `Sec-Fetch-Site: cross-site` отклоняется. Клиент без `Origin` допускается при локальном `Host`. Отказ — `403 forbidden_origin`; это не API для удалённого доступа или произвольного CORS. Для `/api/clipper/v1/` действует отдельный Bearer/Origin guard, описанный ниже.

Заметки и **все Git-маршруты**, включая probe и status, требуют завершённой первичной настройки (`428 setup_required`). `/api/info`, `/api/config`, `/api/setup`, `/api/bases`, `/api/bases/switch` и выбор каталога не обёрнуты в `RequireSetup`, но проверяют свои условия в сервисах. Неподдерживаемый метод возвращает `405 method_not_allowed` и заголовок `Allow`.

`id`, `parent_id`, `path` заметки, пути файлов и назначения разрешения конфликтов относительны **корню выбранной базы**, а не проекту или каталогу конфигурации. Они не могут выходить из базы, быть абсолютными или обращаться к компонентам `.git`; символьная ссылка не позволяет обойти границу базы. Путь самой базы в настройках, `base_path` и `repository_path` — пути каталогов файловой системы. Значения query-параметров необходимо URL-кодировать.

### Глобальная граница служебного хранения

Уже применяется на startup, `POST /api/setup`, добавлении/изменении баз, `PUT /api/config` и переключении базы. Корень базы не может содержать каталог конфигурации, SQLite `metadata.db` и её sidecars (`-wal`, `-shm`, `-journal`, super-journal), либо отдельный `<appDataDir>/clipper-staging/`; база также не может совпадать с приватным каталогом или находиться внутри него. Каталоги конфигурации и staging зарезервированы целиком. `appDataDir` — `~/.igonotes`; штатный `<appDataDir>/bases/default` разрешён. Поле `base_dir` является контейнером: проверяются фактические `bases[].path`, включая неактивные.

Проверка учитывает native separators, регистр Windows, канонические пути, идентичность доступных файлов и symlink aliases существующих предков ещё не созданных путей. Отказ — `422 {"code":"invalid_base_path","message":"base path overlaps private application storage","field":"path"}` без приватных абсолютных путей. При отказе конфигурация, runtime и пользовательские файлы сохраняются; setup/create отклоняется до создания каталога. Текущие origin/setup guards и их приоритет остаются прежними. Смена конфигурации не может включить постоянный staging в базу во время upload; сетевое чтение не требует глобального lifecycle lock.

Несовместимая существующая конфигурация останавливает startup до открытия SQLite/индексации; `--base` не исключает проверки остальных баз. Автоматического переноса/удаления нет: остановите приложение, сделайте резервную копию, вручную отделите каталог заметок от служебных данных и исправьте все несовместимые `bases[].path` в `config.json`; при необходимости вынесите `--config` из базы и обновите параметры запуска. Подробности — в руководстве пользователя. Это не синхронизация с внешними процессами, подменяющими файловую систему. HTTP text import/status Web Clipper доступны; staging зарезервирован постоянно, даже когда временных запросов нет.

## Таблица маршрутов

| Метод | Полный путь | Успех | Назначение |
|:---|:---|:---|:---|
| GET | `/api/notes` | 200 | Дерево заметок активной базы |
| POST | `/api/notes` | 200 | Создать пустую заметку или папку |
| GET | `/api/note?id=<path>` | 200 | JSON: `id`, `content`, `revision` |
| DELETE | `/api/note?id=<path>` | 200, без тела | Удалить файл или папку с содержимым |
| POST | `/api/save` | 200 | Сохранить содержимое с проверкой ревизии |
| PUT | `/api/rename` | 200 | Переименовать файл или папку |
| POST | `/api/sync` | 200 | `SyncFS`: пересканировать файлы и обновить SQLite |
| GET | `/api/info` | 200 | Путь активной базы, название и версия приложения |
| GET | `/api/raw?path=<path>` | 200 | Сырой файл из базы (`ServeContent`, возможны 206/304) |
| POST | `/api/assets` | 200 | Загрузить файл изображения |
| GET | `/api/config` | 200 | Конфигурация приложения |
| PUT | `/api/config` | 200 | Заменить конфигурацию приложения |
| POST | `/api/setup` | 200 | Завершить первичную настройку |
| POST | `/api/bases` | 200 | Создать или подключить базу |
| PUT | `/api/bases?name=<name>` | 200 | Изменить имя или путь базы |
| DELETE | `/api/bases?name=<name>` | 200 | Забыть базу |
| POST | `/api/bases/switch` | 200 | Переключить активную базу |
| POST | `/api/system/select-directory` | 200 или 204 | Выбрать каталог или отменить диалог |
| POST | `/api/git/probe` | 200 | Проверить локальный и удалённый репозитории без изменений |
| PUT | `/api/git/config?base=<name>` | 202 | Сохранить Git-настройки и поставить initialize в очередь |
| DELETE | `/api/git/config?base=<name>` | 200 | Отключить Git, сохранив файлы и `.git` |
| GET | `/api/git/status?base=<name>` | 200 | Статус одной базы; без `base` — всех баз |
| POST | `/api/git/sync?base=<name>` | 202 | Ручная Git-синхронизация |
| POST | `/api/git/resume?base=<name>` | 202 | Явно возобновить синхронизацию после паузы |
| GET | `/api/git/conflicts?base=<name>` | 200 | Текущий снимок конфликтов |
| PUT | `/api/git/conflicts/resolve` | 200 | Разрешить один конфликт |
| POST | `/api/git/conflicts/complete?base=<name>` | 202 | Завершить разрешённое слияние |
| POST | `/api/git/conflicts/abort?base=<name>` | 202 | Отменить слияние и приостановить синхронизацию |
| GET | `/api/clipper/connections` | 200 | Публичные метаданные подключений, включая отозванные |
| POST | `/api/clipper/connections` | 201 | Создать подключение и один раз выдать секрет |
| DELETE | `/api/clipper/connections?id=<id>` | 204, без тела | Отозвать подключение |
| GET | `/api/clipper/v1/info` | 200 | Версия протокола и приложения, лимиты и MIME |
| GET | `/api/clipper/v1/bases` | 200 | Имена настроенных баз |
| GET | `/api/clipper/v1/folders?base=<name>` | 200 | Папки выбранной базы и ревизия назначения |
| POST | `/api/clipper/v1/imports` | 201 / 200 / 202 | Новый import с изображениями или без / успешный replay / pending APIError |
| GET | `/api/clipper/v1/imports?id=<uuid>` | 200 | Снимок состояния известной попытки |

`POST /api/sync` обновляет только индекс файловой системы; Git-синхронизация запускается через `POST /api/git/sync`.

## Web Clipper v1 — discovery и HTTP import доступны

Доступны управление подключениями, три discovery GET-маршрута и HTTP import/status `/api/clipper/v1/` с Bearer-доступом, включая публикацию проверенных изображений и автоматическое локальное восстановление импорта при запуске. Chromium-расширение и административные recovery list/abandon endpoints (5.5) остаются **PLANNED**. Расширение пока нельзя установить и отправлять им страницы. DTO и лимиты находятся в `internal/model/clipper.go`: импорт использует `ClipperManifest`, `ClipperImportResult`, `ClipperImportStatusResponse`.

### Доступ и preflight — доступны

`RequireClipper` требует локальный Host (`localhost` или loopback IP). Переданный `Origin` должен быть единственным и иметь точную форму `chrome-extension://` + 32 строчные буквы `a`–`p`, без порта, пути или завершающего `/`. Web Origin, `null`, пустой или несколько Origin отклоняются как `403 forbidden_origin`. Отсутствие Origin допускается, но не отменяет Bearer. `Sec-Fetch-Site: cross-site` само по себе не запрещает запрос расширения.

Обычный запрос требует ровно один заголовок `Authorization: Bearer <secret>` с целым секретом подключения; токен не передаётся в query. Имя схемы Bearer нечувствительно к регистру, секрет не должен содержать пробелы или посторонние символы. Отсутствующий, некорректный, неизвестный или отозванный секрет даёт `401 clipper_unauthorized`. Проверка идёт в порядке Host/Origin → Bearer → метод → setup → handler. Поэтому действующий токен при незавершённой настройке даёт `428 setup_required`; неподдерживаемый метод после авторизации — `405 method_not_allowed`, `Allow: GET, OPTIONS` для discovery и `GET, POST, OPTIONS` для imports. По умолчанию endpoint — `http://127.0.0.1:8080`, порт меняется флагом `--port`.

`OPTIONS` выполняет preflight **без Bearer и без проверки setup**: требуются допустимый extension Origin, ровно один `Access-Control-Request-Method: GET` (для imports также разрешён `POST`) и, если запрошены заголовки, только подмножество `Authorization`, `Content-Type` (имена нечувствительны к регистру). Успех — `204` без тела, с точным `Access-Control-Allow-Origin`, `Access-Control-Allow-Methods` для запрошенного метода и согласованными `Access-Control-Allow-Headers`. Недопустимый origin/метод/набор заголовков — `403 forbidden_origin`. Wildcard и credentials не выдаются; ответы имеют `Vary: Origin`, preflight также варьируется по запрошенным методу и заголовкам.

Bearer Clipper не открывает `/api/config`, `/api/save`, Git API или management: там действуют отдельные `RequireLocalOrigin`/setup guards. Успешные discovery-ответы имеют `Content-Type: application/json` и `Cache-Control: no-store`; текущие guards и preflight не гарантируют `no-store` на всех ответах.

Неизвестные пути `/api/clipper/v1` и `/api/clipper/v1/...`, включая завершающий `/` и дочерние пути discovery/imports, проходят тот же guard: авторизованный GET после setup получает `404 {"code":"not_found","message":"Not found"}`, а не SPA. Неизвестные management-пути `/api/clipper/...` используют same-origin/setup guards и также возвращают JSON `404` после их прохождения. Import handler зарегистрирован ровно на `/api/clipper/v1/imports`.

### Discovery: запросы и ответы — доступны

| Метод / путь | Запрос | HTTP и тело ответа |
|:---|:---|:---|
| `GET /api/clipper/v1/info` | Без тела | `200`, info ниже |
| `GET /api/clipper/v1/bases` | Без тела | `200 {"bases":["work","personal"]}`; только имена, без абсолютных путей/Git config |
| `GET /api/clipper/v1/folders?base=work` | Имя базы в query | `200 {"base":"work","folders":["","research","research/articles"],"destination_revision":"opaque"}` |

`info` возвращает `protocol_version` из модели отдельно от `app_name`/`app_version` запущенного Go-бинарника из `internal/appinfo` (не npm-версию). `dev` — значение сборки разработки. Для будущего клиента несовместимая версия должна прекращать отправку:

```json
{"protocol_version":1,"app_name":"IGoNotes","app_version":"dev","limits":{"markdown_bytes":5242880,"assets":100,"asset_bytes":10485760,"total_asset_bytes":52428800,"multipart_bytes":67108864,"image_pixels":40000000,"gif_frames":100,"gif_total_pixels":200000000},"supported_image_types":["image/png","image/jpeg","image/gif","image/webp"]}
```

Объявленные лимиты включительные: Markdown — 5 MiB точных декодированных UTF-8 байтов, максимум 100 assets, каждое изображение — 10 MiB, сумма изображений — 50 MiB, всё HTTP multipart-тело вместе с JSON escaping overhead, manifest, MIME-заголовками и разделителями — 64 MiB (`1 MiB = 1048576` байт). `image_pixels` — 40000000 пикселей на изображение, логический canvas GIF и каждый его кадр; `gif_frames` — 100 кадров; `gif_total_pixels` — 200000000 пикселей суммарно по кадрам одного GIF. Превышение любого бюджета — `413 clipper_too_large`. JSON-часть может превышать 5 MiB, если декодированный Markdown укладывается в лимит. Поддерживаются статические PNG/JPEG/WebP и GIF, включая анимацию; APNG, animated WebP и SVG/PDF дают `415 clipper_unsupported_image`. Корректные вложения публикуются вместе с Markdown через HTTP import. Старый лимит `/api/assets` не меняется.

`bases` возвращает только имена в порядке конфигурации. Пустые списки сериализуются как `[]`, не `null`. Discovery не выдаёт абсолютные пути, Git-настройки/секреты, токен/digest подключения или содержимое заметок.

`folders` требует ровно один явно заданный непустой `base`, без подстановки активной базы. Отсутствующий, пустой/пробельный, повторный `base` или некорректный query — `400 clipper_invalid_request`; максимум 1024 байта значения и 8192 байта raw query, превышение также даёт `400`. Имя ищется точно, без trimming; неизвестная база — `404 clipper_destination_not_found`. Невозможность безопасно открыть/обойти корень, включая исчезнувший корень, — `409 clipper_destination_changed`, без внутренних путей или частичного списка.

Список папок отсортирован лексикографически, содержит только существующие относительные папки, видимые в дереве заметок, и обязательно корень `""`, с `/` как разделителем, без завершающего `/`. Символьные ссылки, все каталоги с именем, начинающимся с `.`, и каталоги `assets` без учёта регистра пропускаются вместе со всем поддеревом на любом уровне вложенности. Например, `.hidden`, `ASSETS`, `nested/.cache/deep` и `nested/assets/x` не предлагаются. Можно выбрать неактивную базу: discovery не читает Markdown, не переключает `current_base` и не меняет конфигурацию или индекс заметок.

### Ревизия назначения и повторное подтверждение

`destination_revision` — случайный непрозрачный handle привязки имени базы к каноническому пути и физической идентичности корня, не путь и не ревизия текста. Process-local `ClipperDestinationService` повторно выдаёт тот же handle при неизменной привязке. Успешные rename, смена пути/rebind и forget инвалидируют прежнюю привязку; возврат конфигурации к прежнему имени/пути (ABA) её не восстанавливает. Изменение физической идентичности корня делает прежний handle устаревшим. Переключение активной базы или изменение только Git-настроек не инвалидирует неизменную привязку.

Новый registry, в том числе после **перезапуска IGoNotes**, не принимает старые handles. Для **нового импорта** клиент обязан заново получить bases/folders и revision и явно запросить подтверждение базы/папки у пользователя; нельзя молча заменить revision или перенаправить отправку. Проверка устаревшей revision при новой публикации возвращает `409 clipper_destination_changed`. Импорт не переключает активную базу и не создаёт родительские папки.

Исключение durable replay: точный replay уже успешно завершённой попытки сначала ищет устойчивый receipt и возвращает исторический result **до текущей проверки назначения/revision и политики видимости parent**, в том числе после restart. Это не новый импорт: uncertain attempt сохраняет прежние ID и payload, а не получает свежую revision автоматически. Строгая проверка manifest/digest и авторизация остаются обязательными.

### Импорт: запросы и ответы — доступны для текста

| Метод / путь | Запрос | HTTP и тело ответа |
|:---|:---|:---|
| `POST /api/clipper/v1/imports` | Multipart, описанный ниже | `201` и result при новой публикации; `200` и тот же result при завершённом успешном replay |
| `GET /api/clipper/v1/imports?id=550e8400-e29b-41d4-a716-446655440000` | UUID в query | `200` и снимок состояния ниже; состояние не кодируется HTTP-статусом этого GET |

Сервер не загружает URL из manifest и не генерирует title/frontmatter: клиент передаёт уже подготовленный Markdown, включая нужные YAML-метаданные.

### Multipart и manifest — Markdown и изображения

POST принимает только `multipart/form-data; boundary=...`, без query и без дополнительных параметров outer Content-Type. Ровно одна часть `manifest`: `Content-Disposition: form-data; name="manifest"` и `Content-Type: application/json` (допускается только дополнительный `charset=utf-8`). Бинарные части разрешены в любом порядке, в том числе до manifest: имя части должно точно совпадать с объявленным asset ID, каждая объявленная часть обязательна ровно один раз. MIME бинарной части без параметров должен совпасть с descriptor и фактическим форматом байтов; size и SHA-256 проверяются сервером. Необязательный `filename` игнорируется и не определяет путь на диске. Другие part headers, включая Content-Transfer-Encoding, не допускаются. Голый JSON, отсутствующие/неописанные/повторные части, mismatch size/hash/MIME, повреждённые/обрезанные данные и некорректный envelope дают `400 clipper_invalid_request`. Допускаются как `assets: []`, так и проверенные вложения. Используйте стандартный multipart writer с CRLF: closing delimiter без SP/HTAB padding, затем CRLF либо сразу EOF; LF-only closing и любой epilogue, даже после корректного closing, запрещены. Похожая на delimiter строка внутри бинарных данных сама по себе не является closing.

На точном imports endpoint **все** ответы, включая guards, ошибки и OPTIONS, имеют `Cache-Control: no-store`; JSON-ответы — `Content-Type: application/json`. Host/Origin/Bearer/setup проверяются до чтения тела и создания staging. После полного staging и строгого разбора секрет повторно авторизуется перед вызовом Import: отзыв во время upload даёт `401`, без публикации; operational auth error — безопасный `500`. Приватные временные файлы доступны только до возврата importer и затем удаляются на любом исходе. Отзыв после этой проверки не отменяет уже принятую операцию.

Все десять полей manifest обязательны, `null` не допускается; пустые списки передаются как `[]`. DTO не выполняет эту валидацию автоматически. Пример без вложений:

```json
{"import_id":"550e8400-e29b-41d4-a716-446655440000","base":"work","destination_revision":"opaque","parent":"","filename":"clip.md","markdown":"Hello\n","source_url":"https://example.test/","captured_at":"2026-10-21T12:00:00Z","assets":[],"external_images":[]}
```

Пример descriptors для клипа с изображением и явно подтверждённой внешней ссылкой (sha256 — SHA-256 байтов изображения, не Markdown):

```json
{"assets":[{"id":"a1","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","media_type":"image/png","size":123}],"external_images":[{"url":"https://example.test/unavailable.png","reason":"permission_denied"}]}
```

`import_id` — случайный UUID v4 в lowercase форме `8-4-4-4-12`; asset `id` — уникальная ASCII-строка по `[A-Za-z0-9_-]{1,64}`, кроме зарезервированного `manifest`. `sha256` — 64 lowercase hex символа без префикса; `size` — целое число байтов от 1 до лимита. MIME — ровно одно из четырёх значений info, без параметров. `captured_at` — корректная UTC RFC3339 строка с `Z` (допускаются 1–9 цифр дробной секунды); её исходное строковое представление сохраняется. `source_url` и внешние URL — абсолютные HTTP(S) URL; `reason` — непустая строка причины, не HTML. Пустые строки не заменяются отсутствием поля.

`parent` — существующая относительная папка, видимая в дереве заметок, либо `""`; `filename` — один безопасный компонент с `.md`. Новый импорт с любым компонентом `parent`, начинающимся с `.`, либо равным `assets` без учёта регистра отклоняется как `400 clipper_invalid_request`, включая ручной запрос в активную или неактивную базу и вложенные пути вроде `nested/.cache/deep` и `nested/assets/x`. Эта политика применяется до journal claim и файловых записей. Она не меняет синтаксическую проверку/canonical digest и применяется после поиска receipt: точный replay ранее завершённой попытки возвращает исторический результат. Политика относится только к родительским папкам заметки, не к её filename и не к серверным attachment paths. Абсолютные пути, выход из корня, `.git`, separators/control characters и Windows reserved names/characters отклоняются. Markdown — непустой подготовленный текст; сервер сохраняет его без нормализации окончаний строк.

Изображения публикуются как `assets/images/clip-<import_id>-<sha256>.<verified-ext>` (`png`, `jpg`, `gif`, `webp`); attachment paths не задаются multipart filename. Одинаковые SHA-256/verified-ext внутри одной попытки дают один физический файл, даже при нескольких asset IDs. Существующие файлы никогда не перезаписываются и не принимаются как свои, даже при совпадении байтов. Заметка публикуется последней. Markdown сохраняется точно: клиент заранее подготавливает ссылки, например `../../assets/images/clip-<import_id>-<sha256>.png` для `research/articles/note.md`; сервер не выполняет глобальную замену URL. Поддержка этих относительных путей в preview редактора остаётся задачей 5.7.

До записи в базу importer копирует проверенные bytes из request directory в собственный случайный подкаталог зарезервированного private staging, синхронизирует bytes/каталоги и сохраняет immutable journal v2 с источниками восстановления. Handler всегда удаляет исходный request directory. После устойчивого success receipt или подтверждённого безопасного отказа собственные источники удаляются; при pending/неопределённой публикации сохраняются. Возможен частичный результат: только в исходной критической секции разрешена очистка своих неизменённых assets по native identity и hash. Неопределённый rename (включая EEXIST), изменение файла пользователем, ошибки index/receipt после публикации заметки требуют recovery, а не обещания all-or-nothing.

HTTP handler использует публичный строгий `DecodeClipperManifest` из `internal/service/clip_import_manifest.go`. Decoder требует все поля также внутри asset/external-image объектов, отклоняет `null`, неизвестные и повторные ключи на любом уровне (в том числе escape-эквивалентные), неверные типы и trailing JSON. Вход ограничен 64 MiB, включая JSON escapes; превышение — `clipper_too_large`. Проверка descriptors не проверяет фактические байты изображений и не обращается к сети/файловой системе.

Отдельный `service.StageClip` уже проверяет фактические bytes: потоковый hash/size и приватный spool, структурный preflight до выделения пикселей и полное декодирование (всех GIF-кадров). PNG проверяется до IEND с CRC всех chunks и точным zlib/IDAT-потоком, JPEG — до EOI, WebP — по полному RIFF framing, GIF — до trailer; данные после конца контейнера отклоняются. Исходные изображения не перекодируются, URL не загружаются. Вызов валидатора сам по себе не означает принятие или durable сохранение image import.

Уточнения границ валидации v1: `base` — точное непустое имя без крайних пробелов, как сохранённое SettingsService, без trimming при импорте; `destination_revision` — непрозрачная непустая/непробельная строка. `parent` использует только `/`, без пустых компонентов, `.`/`..`, обратных слешей или завершающего `/`; пустая строка обозначает корень. Каждый компонент пути, включая filename, исключает `.git` без учёта регистра, управляющие Unicode-символы, Windows-символы `<>:"/\|?*`, завершающие точки/ASCII-пробелы и device names (включая имена с расширением, `CONIN$`/`CONOUT$`, `COM1`–`COM9`, `LPT1`–`LPT9` и варианты с `¹²³`). Filename имеет непустую основу и точный lowercase суффикс `.md`. Markdown должен содержать хотя бы один непробельный Unicode-символ; UTF-8 и лимит проверяются по точным байтам.

URL ограничен 8192 UTF-8 байтами: обязательны HTTP(S), host, отсутствие credentials, обратного слеша, пробелов/управляющих символов и percent-encoded управляющих символов/невалидного UTF-8. Fragment разрешён; URL сохраняется точно, включая регистр и escapes. `external_images` содержит максимум 100 элементов (превышение — `clipper_too_large`); `reason` — валидный UTF-8, непустой/непробельный, максимум 1024 байта, без управляющих символов и `<`/`>` (не HTML). Небезопасный/слишком длинный URL или reason — `clipper_invalid_request`. Порядок внешних изображений сохраняется.

### Canonical manifest / request digest v1 — реализован

Digest вычисляется **после строгой валидации** как SHA-256 следующих канонических UTF-8 байтов и хранится как 64 lowercase hex символа без префикса. Он не является полем manifest. Каноническое значение — JSON-массив с фиксированными позициями (имена ниже обозначают значения полей):

```text
["igonotes-clipper",1,import_id,base,destination_revision,parent,filename,markdown,source_url,captured_at,
 [[asset.id,asset.sha256,asset.media_type,asset.size],...],
 [[external.url,external.reason],...]]
```

Правила, обязательные и для Go, и для JavaScript:

1. В этом массиве представлены ВСЕ поля manifest, плюс фиксированные domain separator и версия. Объектов и порядка их свойств в канонических байтах нет; порядок свойств входного JSON не влияет на digest.
2. Asset descriptors сортируются по возрастанию ASCII-байтов `id`, лексикографически (префикс раньше более длинной строки). Исходный порядок `assets` и порядок multipart parts не влияют на digest. `external_images` сохраняет порядок manifest; перестановка этих элементов меняет digest. Пустые массивы — `[]`, не `null`; отсутствующие поля и `null` не нормализуются, а отклоняются.
3. Числа `1` и `size` записываются десятичными ASCII-цифрами без знака, ведущих нулей, дробной части или exponent. Между токенами нет пробелов/переводов строк, нет BOM и завершающего LF.
4. Строки заключаются в ASCII `"`. Экранируются только `"` как `\"`, обратная косая черта как `\\`, U+0008/0009/000A/000C/000D как `\b`, `\t`, `\n`, `\f`, `\r`; остальные U+0000–001F как `\u00xx` с lowercase hex. `/`, `<`, `>`, `&`, U+007F и все остальные Unicode scalar values, включая U+2028/U+2029 и emoji, записываются непосредственно UTF-8. Запрещены невалидный UTF-8 и непарные UTF-16 surrogates; замена на U+FFFD не допускается.
5. Unicode normalization, trimming, URL normalization, изменение регистра, переформатирование даты и перевод CRLF в LF НЕ выполняются. Сохраняются точные значения декодированных строк; `markdown` особенно сохраняется побайтно. Различные JSON escapes входного запроса, обозначающие одну строку, дают одинаковый digest.
6. Digest не включает Bearer, endpoint, multipart boundaries/headers/filename, порядок частей и сами бинарные байты. Он включает их проверенные SHA-256/size/MIME descriptors; сервер самостоятельно проверяет байты. Новый токен не меняет digest. Обычные `encoding/json` и `JSON.stringify` без явного обеспечения этих правил не являются определением канонизации (в частности HTML и Unicode escaping).

Тестовый вектор для полного manifest без вложений выше: канонические байты — следующая **одна строка**, где `\n` — два ASCII-символа escape, без LF после `]`:

```json
["igonotes-clipper",1,"550e8400-e29b-41d4-a716-446655440000","work","opaque","","clip.md","Hello\n","https://example.test/","2026-10-21T12:00:00Z",[],[]]
```

Ожидаемый SHA-256: `4e864bead648d1e1d07e3d4d4695ed9adc270eb13619e0827d858876036b34b0`.

Общие Go/JS vectors: `testdata/clipper/manifest-digests.json`. Каждый элемент содержит `name`, полный `manifest`, строку `canonical` (после JSON-декодирования — точные канонические UTF-8 байты без завершающего LF) и bare hex `digest`. Векторы покрывают пример выше, сортировку assets, Unicode/HTML/control escaping, порядок внешних изображений и точные CRLF/URL/timestamp значения.

### Result, состояния и replay — доступны

Успех POST: `201 Created` для новой публикации, `200 OK` для точного завершённого replay; одинаковое тело result:

```json
{"import_id":"550e8400-e29b-41d4-a716-446655440000","base":"work","path":"clip.md","revision":"sha256:66a045b452102c59d840ec097d59d9467e13a3f34f6494e539ffd32c1bb35f18","warnings":[]}
```

`path` относителен исторической базе; `revision` — SHA-256 записанного текста с префиксом `sha256:`. `warnings` всегда массив строк, в том числе для подтверждённых внешних изображений. Успех означает устойчивый receipt после публикации и обновления индекса активной базы, но не Git push.

GET известного import_id всегда `200`, с одним из следующих тел (сокращённый `id` ниже означает полный UUID):

| Состояние | Тело GET (200) | Поведение точного POST replay |
|:---|:---|:---|
| `pending` | `{"import_id":"id","state":"pending"}` | `202 {"code":"clipper_import_pending","message":"Clipper import pending"}`; затем GET/точный replay |
| `succeeded` | `{"import_id":"id","state":"succeeded","result":{...}}`, result выше | `200` исходный result без повторной записи |
| `rejected` | `{"import_id":"id","state":"rejected","error":{"code":"note_conflict","message":"Note already exists"}}` | Исходный HTTP отказ и исходный APIError (например `409 note_conflict`) |
| `recovery_required` | `{"import_id":"id","state":"recovery_required","error":{"code":"clipper_recovery_required","message":"Clipper import requires recovery"}}` | `409 clipper_recovery_required`; новый ID автоматически не создаётся |

Pending не содержит `result`/`error`; succeeded содержит только `result`; rejected/recovery_required — только `error`. Тот же ID с другим digest всегда даёт `409 clipper_request_changed`, не заменяя прежний receipt. Завершённый replay ищется **до** проверки устаревшего destination handle: результат доступен после restart/rename базы/смены токена в той же установке, без проверки текущего файла и без восстановления впоследствии удалённых файлов. Result описывает историческое назначение. Неизвестный либо некорректный ID при GET — `400 clipper_invalid_request`; отсутствие receipt не доказывает, что конкурентный POST не завершится.

Timeout, сетевой обрыв и 5xx сами по себе означают неопределённый исход: сохранять immutable attempt, включая ID, весь payload и исходную destination_revision, и запрашивать GET/повторять точно тот же POST. Не удалять expected checks, не менять базу/папку/filename/Markdown/revision для обхода отказа и не создавать новый ID, пока исход не разрешён. `pending` и `recovery_required` не являются rejected; `400` на GET неизвестного/некорректного ID не доказывает отсутствие ещё выполняющегося POST. Только подтверждённый rejected (явный окончательный отказ POST либо снимок `state: rejected`, в том числе с `internal_error`) позволяет исправить запрос и создать новый ID после повторного подтверждения назначения. Один `500 internal_error` такого подтверждения не даёт.

Private SQLite journal хранит точный Markdown, `prepared`/`publishing`, checkpoints и durable receipt; незавершённые состояния отображаются как `pending`/`recovery_required`, новые публикации в корень с unresolved import блокируются. При запуске `ClipImportService.RecoverLocal(ctx) error` вызывается после local Git recovery, до GitManager.Start, первичной фоновой индексации и HTTP. Ошибка БД или отмена recovery прерывает запуск с безопасной ошибкой без приватной диагностики; durable per-record `recovery_required` не останавливает сервер, доступен через прежний Bearer GET status и не блокирует обычную работу несвязанных баз. Новых HTTP recovery-маршрутов нет.

Recovery сопоставляет журнал с текущим настроенным физическим корнем, native identity, SHA/size всех note/assets/temp и собственных private sources. Перемещение/переименование того же корня с обновлением настройки допустимо; receipt сохраняет исторические base/path и revision `sha256:<hex>`. Чужие/изменённые файлы, неизвестные схемы, потерянные источники и недоказуемое владение дают durable `clipper_recovery_required` без перезаписи/удаления public files; Git conflict также запрещает изменения worktree. Ошибки БД и отмена возвращаются вызывающему коду, остальные записи могут обрабатываться независимо. Terminal receipts не перепроверяются и не восстанавливают удалённые позднее файлы.

Административные list/abandon endpoints и recovery UI также PLANNED (5.5); будущий abandon должен завершать попытку как rejected с `clipper_import_abandoned`, не удаляя файлы и не допуская повторную публикацию прежнего ID.

Новые private plans для text/image imports до файловых эффектов сохраняют ordered `warnings`, включая дубликаты и явный пустой массив. Receipt использует именно эту durable metadata. Отсутствующее поле или `null` в старом незавершённом плане означает неизвестные warnings, а не `[]`; local recovery оставляет такую попытку для ручного вмешательства. Завершённые исторические receipts воспроизводятся без изменений.

### Ошибки Clipper

Форма — существующий `APIError {code,message,field?}`. Пример: `409 {"code":"clipper_destination_changed","message":"Clipper destination changed"}`. Типизированные Clipper errors получают фиксированные безопасные сообщения; внутренние пути, токены и wrapped diagnostics не выдаются.

Discovery и import используют коды ниже; origin/method/namespace ошибки описаны выше. `clipper_import_abandoned` зарезервирован для будущего abandon endpoint. `clipper_unsupported_image` возможен при проверке descriptor, MIME и неподдерживаемой анимации.

| HTTP | code | Значение / пример message |
|:---|:---|:---|
| 401 | `clipper_unauthorized` | `Clipper authorization failed` |
| 400 | `clipper_invalid_request` | `Invalid Clipper request`; JSON/manifest/hash/part mismatch или неизвестный ID GET |
| 413 | `clipper_too_large` | `Clipper request exceeds limits` |
| 415 | `clipper_unsupported_image` | `Unsupported Clipper image` |
| 404 | `clipper_destination_not_found` | `Clipper destination not found` |
| 409 | `clipper_destination_changed` | `Clipper destination changed` |
| 409 | `note_conflict` | `Note already exists`; существующий файл не перезаписывается |
| 409 | `git_conflict_pending` | `git conflict pending`; существующее значение service gate |
| 409 | `clipper_request_changed` | `Clipper request changed` |
| 409 | `clipper_recovery_required` | `Clipper import requires recovery`; не окончательный отказ |
| 409 | `clipper_import_abandoned` | `Clipper import abandoned; files may already exist` |
| 202 | `clipper_import_pending` | `Clipper import pending`; ещё не успех и не отказ |
| 428 | `setup_required` | `setup required`; существующий setup gate |
| 500 | `internal_error` | `Internal server error`; неизвестная ошибка, не подтверждённый rejected |

## Подключения в локальных настройках — доступны

В интерфейсе: `Настройки` → `Веб-клиппер`, перед вкладкой `О программе`. Доступны список, создание по названию, одноразовый показ и скрытие секрета, отзыв, состояния загрузки и ошибки с повторной загрузкой списка. Секрет копируется вручную; по спецификации его предстоит вводить в настройки будущего расширения вместе с endpoint. Это не означает, что расширение уже доступно для установки.

Management использует существующие `RequireLocalOrigin` и `RequireSetup`: локальный Host, совпадение переданного Origin по схеме/хосту/порту, отказ для `Sec-Fetch-Site: cross-site`, завершённый setup. `localhost` и `127.0.0.1` не взаимозаменяемы для same-origin, в том числе на пользовательском порту. Без Origin запрос с локальным Host допустим. Bearer здесь не требуется и не обходит guards: Origin расширения получает `403 forbidden_origin` даже с действующим токеном; незавершённая настройка для поддерживаемого метода — `428 setup_required`.

Зарегистрирован ровно путь `/api/clipper/connections`, без завершающего `/` и дочерних маршрутов. Неподдерживаемые методы после origin-проверки возвращают `405 method_not_allowed`, `Allow: DELETE, GET, POST`.

| Метод / путь | Запрос | Ответ |
|:---|:---|:---|
| `POST /api/clipper/connections` | `{"label":"Laptop"}` | `201 {"connection":{"id":"connection-id","label":"Laptop","created_at":"2026-10-21T12:00:00Z","revoked_at":null},"secret":"<one-time-secret>"}`, `Cache-Control: no-store` |
| `GET /api/clipper/connections` | Без тела | `200 {"connections":[{"id":"connection-id","label":"Laptop","created_at":"2026-10-21T12:00:00Z","revoked_at":null}]}` |
| `DELETE /api/clipper/connections?id=connection-id` | Без тела | `204`, без тела |

Имена полей и статусы соответствуют handler fixtures; `connection-id`, `<one-time-secret>` и время в таблице — иллюстративные значения. Реальный ID — 32 lowercase hex символа. Выданный непрозрачный секрет имеет форму `<IDhex32>.<random-base64url43>`: ID, точка и 32 случайных байта (256 бит) в base64url без padding. Клиент переносит всю строку целиком. SQLite хранит SHA-256 digest всей строки, не исходный секрет; в Config и URL токен не помещается. GET не выдаёт ни секрет, ни digest, пустой список — `{"connections":[]}`.

POST принимает один JSON-объект с `label`, максимум **4 KiB (4096 байт)** тела. Неизвестные поля, trailing JSON/данные, `null`, неверные типы и превышение лимита отклоняются как `400 {"code":"clipper_invalid_request","message":"Invalid Clipper request"}` (не `413`). Название — непустая после trimming строка, до 128 Unicode-символов до trimming, без управляющих символов; крайние пробелы удаляются. `Cache-Control: no-store` устанавливается обработчиком создания и на его ошибках.

DELETE требует ровно один непустой `id`; отсутствующий, пустой/пробельный, повторный или неизвестный ID и некорректный query возвращают тот же `400 clipper_invalid_request`. Неизвестные внутренние ошибки — `500 {"code":"internal_error","message":"Internal server error"}`, без внутренних диагностик.

`created_at`/`revoked_at` — UTC RFC3339 timestamps, возможны дробные секунды; `revoked_at: null` означает активное подключение. Отозванная metadata остаётся в списке. Повторный DELETE существующего отозванного ID также возвращает `204`, сохраняя **первую** дату отзыва. После отзыва секрет получает `401 clipper_unauthorized` на discovery/import/status. POST повторно проверяет секрет после staging; отзыв после этой проверки не отменяет уже принятую публикацию.

При потерянном ответе POST, timeout или 5xx подключение могло уже сохраниться. Обновите список, отзовите лишнюю/нераспознанную запись, затем **явно** создайте новое подключение, если нужно. Автоповтор POST создаёт ещё одно подключение, а не возвращает прежний секрет. Утерянный секрет не восстанавливается; новое подключение получает новый ID и секрет, даже с тем же label.

Область `Authorization: Bearer <secret>` ограничена Clipper v1: info, имена баз/папок, text import и статус. `<secret>` — очевидный placeholder, не рабочий секрет; используйте целую строку, выданную при создании подключения.

Он не предоставляет доступ к существующим API заметок, Config, Git или management, не меняет их origin/setup правила и не служит авторизацией удалённого сервера.

## Заметки, файлы и ревизии

### `GET /api/notes` и `POST /api/notes`

GET возвращает массив узлов; пустая база — `[]`. `id` и `path` — относительный путь, `name` файла — заголовок без расширения, `type` — `file` или `dir`. Пустые `children` и корневой `parent_id` опускаются.

```json
[
  {
    "id": "notes",
    "name": "notes",
    "type": "dir",
    "path": "notes",
    "children": [
      {"id": "notes/idea.md", "name": "idea", "type": "file", "path": "notes/idea.md", "parent_id": "notes"}
    ]
  }
]
```

POST создаёт пустой файл (расширение `.md` добавляется при необходимости) или папку. `parent_id: ""` означает корень, для вложенного узла указывается путь существующей папки. Содержимое сохраняется отдельным запросом.

```json
{"parent_id":"notes","name":"new.md","type":"file"}
```

Ответ `200 OK`:

```json
{"id":"notes/new.md","name":"new","type":"file","path":"notes/new.md","parent_id":"notes"}
```

### `GET /api/note?id=notes%2Fidea.md`

Ответ — JSON, включая ревизию точных байтов файла (SHA-256, изменения окончаний строк тоже меняют ревизию):

```json
{"id":"notes/idea.md","content":"hello\n","revision":"sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"}
```

### `POST /api/save`

Официальный интерфейс **всегда передаёт `expected_revision`** из последнего GET или успешного save:

```json
{"id":"notes/idea.md","content":"world\n","expected_revision":"sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"}
```

Ответ `200 OK` содержит новую ревизию записанных байтов:

```json
{"status":"saved","revision":"sha256:e258d248fda94c63753607f7c4494ee0fcbe92f1a76bfdac795c9d84101eb317"}
```

Если файл уже изменился на диске или при Git-синхронизации, сервер ничего не перезаписывает и возвращает `409 Conflict`:

```json
{"code":"note_changed","message":"note changed"}
```

Клиент сохраняет локальный черновик, делает **свежий GET** заметки, показывает актуальное содержимое и получает **явное подтверждение пользователя** на объединение или замену. Только затем выполняется **второй revision-aware save** с ревизией свежего GET. Повторный `note_changed` требует нового чтения и подтверждения; автоматически повторять старый запрос или убирать ревизию нельзя. Пропуск `expected_revision` оставлен только для совместимости со старыми клиентами и означает безусловную запись; переданная пустая строка считается несовпадающей ревизией.

Например, если после отказа свежий GET вернул `content: "world\n"` и ревизию `sha256:e258d248fda94c63753607f7c4494ee0fcbe92f1a76bfdac795c9d84101eb317`, после подтверждения объединённого текста второй запрос выглядит так:

```json
{"id":"notes/idea.md","content":"merged\n","expected_revision":"sha256:e258d248fda94c63753607f7c4494ee0fcbe92f1a76bfdac795c9d84101eb317"}
```

Ответ `200`:

```json
{"status":"saved","revision":"sha256:72d8264b97bee169d1844d054282694b68be7e91c8bd5d616540adf63ae7d4af"}
```

### Переименование, удаление и индекс

`PUT /api/rename`:

```json
{"id":"notes/idea.md","new_name":"plan.md"}
```

Ответ: `{"status":"renamed"}`. `DELETE /api/note?id=notes%2Fplan.md` возвращает `200` без тела. Создание и переименование при занятом имени возвращают `409 note_conflict`.

`POST /api/sync` выполняется без JSON-тела и возвращает `{"status":"ok"}` после пересканирования. `GET /api/info` возвращает, например, `{"base_path":"/home/user/notes/work","app_name":"IGoNotes","app_version":"v0.0.1-alpha.6"}`. Все три поля — строки; до настройки `base_path` может быть пустым. `app_version` содержит тег релизной сборки либо `dev` для сборки разработки. Название и версия относятся к запущенному Go-бинарнику.

### Файлы и изображения

`GET /api/raw?path=assets%2Fimages%2Flogo.png` отдаёт байты файла, не JSON. `POST /api/assets` принимает multipart-поле **`file`**; ограничение **10 MiB относится ко всему HTTP-телу, включая multipart framing**. Успех `200`:

```json
{"path":"assets/images/Paste image 20260714150000.png"}
```

При ожидающем Git-конфликте обычные save/create/rename/delete/upload блокируются с `409 git_conflict_pending`, чтобы не менять конфликтное рабочее дерево в обход resolver.

## Настройки, базы и выбор каталога

`GET /api/config` возвращает непосредственно `Config`:

```json
{
  "base_dir": "/home/user/notes",
  "bases": [{"name":"work","path":"/home/user/notes/work","auto_sync":false}],
  "current_base": "work",
  "setup_completed": true
}
```

`PUT /api/config` принимает такую же полную конфигурацию и возвращает `SettingsResponse`, как и операции setup/bases/switch:

```json
{
  "config": {
    "base_dir": "/home/user/notes",
    "bases": [{"name":"work","path":"/home/user/notes/work","auto_sync":false}],
    "current_base": "work",
    "setup_completed": true
  },
  "base_path": "/home/user/notes/work"
}
```

Git-поля существующих баз можно сохранить неизменными в GET→PUT, но менять их или задавать для новой базы через общий `/api/config` нельзя (`422 invalid_config`); для этого служит `/api/git/config`.

`POST /api/setup` и `POST /api/bases` принимают `mode` (`create` — создать, `connect` — подключить существующий каталог), `name` и `path`:

```json
{"mode":"connect","name":"work","path":"/home/user/notes/work"}
```

Setup завершает первичную настройку и активирует первую базу; повторный setup возвращает `409 setup_already_completed`. Добавление базы не переключает текущую. `PUT /api/bases?name=work` принимает:

```json
{"name":"work-renamed","path":"/home/user/notes/work"}
```

`POST /api/bases/switch` принимает `{"name":"work-renamed"}`. `DELETE /api/bases?name=work` забывает запись, **не удаляя каталог или файлы**; активную или последнюю базу забыть нельзя. Перед переключением официальный интерфейс завершает загрузки и сохраняет текущую заметку; ошибка сохранения блокирует переход.

`POST /api/system/select-directory` не требует JSON-тела. При выборе ответ `200` — `{"path":"/home/user/notes"}`; отмена — `204 No Content` без тела. Недоступный системный диалог — `501 directory_picker_unavailable`, ошибка диалога — `500 directory_picker_failed`.

## Git: проверка и подключение

Git требует установленного Git **2.28+**, настроенной identity и доступной аутентификации через окружение пользователя (например, SSH agent или credential helper). Приложение не запрашивает пароль через терминал. HTTP(S) URL с credentials, URL с query/fragment и неподдерживаемые helper-схемы отклоняются. `git_branch` — буквальное имя ветки, например `main`, а не `HEAD`, OID или `refs/heads/main`.

Для query-маршрутов Git допустим только `base`, ровно один раз. Он обязателен кроме GET status; неизвестные/повторные query-параметры дают `400 bad_query`. В probe и resolve имя базы передаётся в JSON.

### `POST /api/git/probe`: два прохода

Probe не изменяет конфигурацию, `.git`, refs, рабочее дерево или remote. Первый проход без ветки проверяет URL/доступность и обнаруживает ветки:

```json
{"base":"work","git_url":"git@example.test:notes.git"}
```

Даже без блокирующей ошибки этот discovery-ответ имеет `can_configure: false` и `history_relation: "unknown"`. После выбора ветки обязателен второй probe:

```json
{"base":"work","git_url":"git@example.test:notes.git","git_branch":"main"}
```

Полный пример `200 OK` для существующего репозитория:

```json
{
  "base": "work",
  "git_version": "git version 2.50.0",
  "has_repository": true,
  "repository_root": "/home/user/notes/work",
  "repository_root_matches": true,
  "current_branch": "main",
  "detached_head": false,
  "working_tree_clean": true,
  "existing_origin_url": "git@example.test:notes.git",
  "remote_branches": ["main", "topic"],
  "empty_remote": false,
  "identity_configured": true,
  "history_relation": "shared",
  "can_configure": true,
  "required_mutations": {
    "create_repository": false,
    "add_origin": false,
    "replace_origin": false,
    "create_branch": false,
    "merge_histories": false
  },
  "warnings": ["Git commits include every non-ignored file in the base directory."]
}
```

Все boolean-поля и пять `required_mutations` передаются явно. `remote_branches` и `warnings` — массивы, включая пустые. `repository_root`, `current_branch`, `existing_origin_url`, `pending_operation`, `blocking_error` опускаются, если отсутствуют. Небезопасный существующий origin не раскрывается и блокирует настройку. `pending_operation` может быть `merge`, `rebase`, `cherry-pick` или `revert`; это блокирующий факт. `working_tree_clean: false` сам по себе не запрещает подключение.

`history_relation`: `none` — нет истории для сравнения; `shared` — общий предок; `unrelated` — общего предка нет; `unknown` — нельзя доказать связь локальными объектами (в том числе shallow/partial repository или отсутствующий remote commit). Probe не делает fetch для этого сравнения. `unrelated` и `unknown` при выбранной ветке требуют `merge_histories` и добавляют предупреждение `Connecting may merge existing local and remote histories.`

`remote_branches` содержит отсортированные имена только `refs/heads/*`. **`empty_remote` означает отсутствие всех advertised refs**, а не пустой список веток: remote с одними tags не считается пустым. Для действительно пустого remote требуется `create_branch`; отсутствующая выбранная ветка непустого remote блокируется (`invalid_branch`).

Проверка может успешно ответить `200`, но сообщить отказ через `blocking_error`, например `{"code":"identity_missing","message":"Git identity is not configured"}` и `can_configure: false`. Ошибки самого запроса (неизвестная база, неверный URL) возвращаются как HTTP-ошибки. Клиент проверяет оба поля, показывает предупреждения и получает подтверждения требуемых изменений.

### `PUT /api/git/config?base=work`

```json
{
  "git_url": "git@example.test:notes.git",
  "git_branch": "main",
  "auto_sync": true,
  "auto_sync_interval_minutes": 15,
  "git_commit_message_template": "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)",
  "confirmations": {
    "create_repository": false,
    "replace_origin": false,
    "create_branch": false,
    "merge_histories": false
  }
}
```

Четыре подтверждения соответствуют одноимённым `required_mutations`; требуемые флаги становятся `true` только после явного согласия пользователя. `add_origin` отдельного подтверждения не имеет. Подтверждения относятся к этой initialize-операции и **не сохраняются** в конфигурации. Worker повторно проверяет состояние; принятие запроса не гарантирует успех подключения.

Интервал — **5, 15, 30 или 60 минут**. Официальный интерфейс по умолчанию отправляет **15**; API не подставляет 15 вместо отсутствующего поля: при `auto_sync: true` значение `0` недопустимо, при `false` допустимо `0` или один из четырёх интервалов. Пустой шаблон заменяется сервером на `IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)`. Допустимы `{{base}}`, `{{branch}}`, `{{date}}`, `{{datetime}}`, `{{count}}`; шаблон ограничен 200 символами, не может состоять из пробелов, содержать управляющие символы или неизвестные/непарные переменные.

В поставляемом приложении PUT сначала сохраняет Git-настройки, затем пытается поставить initialize в очередь, в том числе при изменении autosync/интервала/шаблона. **`202 Accepted` означает, что операция принята**, а не что initialize завершён. Ответ — обязательная обёртка **`base`, `status`, `operation`**, не одиночный operation:

```json
{
  "base": {
    "name": "work",
    "path": "/home/user/notes/work",
    "git_url": "git@example.test:notes.git",
    "git_branch": "main",
    "auto_sync": true,
    "auto_sync_interval_minutes": 15,
    "git_commit_message_template": "IGoNotes: sync {{base}} at {{datetime}} ({{count}} files)"
  },
  "status": {
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "initializing",
    "operation_id": "0123456789abcdef0123456789abcdef",
    "stage": "queued",
    "ahead": 0,
    "behind": 0,
    "consecutive_failures": 0,
    "changed_paths": []
  },
  "operation": {"operation_id":"0123456789abcdef0123456789abcdef","status":"queued","deduplicated":false}
}
```

Статус в ответе — актуальный снимок: worker может успеть перейти дальше `queued`. За результатом необходимо следить через GET status. Initialize может создавать репозиторий, origin, ветку и объединять истории в пределах подтверждённых изменений; commit включает **все неигнорируемые файлы базы**, не только Markdown.

**Ошибка постановки initialize в очередь возвращает non-202, но уже сохранённые Git-настройки остаются в конфигурации**: обработчик не откатывает их при отказе `QueueInitialize`. Клиент должен перечитать `GET /api/config` и `GET /api/git/status?base=work`, показать фактическое состояние и безопасную ошибку, а не считать настройки несохранёнными или вслепую откатывать их повторным PUT/DELETE. Non-202 сам по себе также не доказывает отсутствие принятой операции: ошибка чтения статуса после успешной постановки в очередь возвращается уже после её принятия. Повторное действие выбирается по свежим config/status; при `202` клиент отслеживает принятую операцию до итогового состояния.

### `DELETE /api/git/config?base=work`

Без тела запроса. Очищает Git-поля приложения, сохранённый статус и расписание; не удаляет `.git` или пользовательские файлы. Ответ `200` без `operation`:

```json
{
  "base": {"name":"work","path":"/home/user/notes/work","auto_sync":false},
  "status": {"base":"work","repository_path":"/home/user/notes/work","state":"unconfigured","ahead":0,"behind":0,"consecutive_failures":0,"changed_paths":[]}
}
```

## Git: операции, статусы и возобновление

`POST /api/git/sync?base=work`, `POST /api/git/resume?base=work`, `POST /api/git/conflicts/complete?base=work` и `POST /api/git/conflicts/abort?base=work` отправляются **без тела**, с базой в query. Их `202 Accepted` — самостоятельный `GitOperationResponse`:

```json
{"operation_id":"44444444444444444444444444444444","status":"queued","deduplicated":false}
```

`operation_id` идентифицирует принятую долговечную операцию, `status` — состояние операции (`queued`, `running`, `succeeded`, `failed`, `conflict`), `deduplicated: true` означает возврат уже существующей операции. Это не `state` базы и не обещание завершения; клиент опрашивает GET status. Операции выполняются одним последовательным worker, дедупликация основана на канонической идентичности репозитория. Обычный sync не обходит конфликт, паузу или необходимость переподключения.

### `GET /api/git/status`

Ответ всегда `{"statuses":[...]}`: без `base` — все базы в порядке конфигурации, включая ненастроенные; `?base=work` — массив из одного элемента. Неизвестная база — `404 base_not_found`.

| `state` | Значение |
|:---|:---|
| `unconfigured` | Git не настроен |
| `initializing` | Подключение поставлено в очередь или выполняется |
| `ready` | Последняя операция успешна |
| `syncing` | Синхронизация поставлена в очередь или выполняется |
| `error` | Безопасная операционная ошибка; следующая попытка может быть разрешена |
| `paused` | Требуется явный resume |
| `conflict` | Требуется разрешение или отмена слияния |
| `needs_reconnect` | Требуется повторное подключение через Git-настройки |

В `GitStatus` всегда присутствуют `base`, `state`, `ahead`, `behind`, `consecutive_failures`, `changed_paths`. Опциональны `repository_path`, `operation_id`, `stage`, `last_attempt`, `last_success`, `remote_oid`, `error`. `ahead`/`behind` — сохранённые счётчики commit, timestamps — RFC3339, `remote_oid` — сохранённый доверенный OID remote, `changed_paths` — массив относительных путей, изменённых операцией (не live porcelain-status). Пустые пути — `[]`. `error` содержит только безопасные `code`, `message` и, при наличии, `field`.

Стадии: `queued`, `probing`, `fetching`, `snapshotting`, `backing_up`, `switching`, `merging`, `reindexing`, `pushing`, `completed`, `conflict_resolving`, `conflict_completing`, `conflict_committed`, `conflict_reindexed`, `conflict_pushing`, `conflict_aborting`. При отказе сохраняется последняя достигнутая стадия; `completed` не выставляется для неуспешной операции.

### Планирование и счётчик ошибок

Автосинхронизация работает для всех настроенных баз с `auto_sync: true`, включая неактивные. Допустимые интервалы — **5, 15, 30 и 60 минут**. После запуска просроченные базы и базы без предыдущей попытки получают стартовые слоты с шагом пять секунд в порядке конфигурации; первый слот — через пять секунд. Следующий срок отсчитывается от сохранённого `last_attempt`. Ручные и автоматические операции используют один FIFO worker и одинаковую дедупликацию по каноническому пути репозитория.

Только последовательные операционные ошибки sync и conflict complete увеличивают `consecutive_failures`. Первые четыре оставляют `error` и допускают следующую автоматическую попытку. Пятая атомарно сохраняет `paused` и счётчик `5`; автоматических попыток больше нет. Удалённая ветка или переписанная remote history могут немедленно вызвать безопасную паузу без увеличения счётчика.

Ошибки валидации, отклонение запроса до постановки в очередь, устаревшая конфигурация, merge conflict, успешный или неуспешный conflict abort и отмена при shutdown **не увеличивают** счётчик. Ошибки initialize также не расходуют бюджет автосинхронизации. При shutdown выполняющаяся операция получает безопасную ошибку `operation_interrupted`; следующая операция не запускается, а заключительного commit/push нет.

`consecutive_failures`, `last_attempt`, `last_success`, пауза и безопасная ошибка сохраняются в SQLite и переживают перезапуск. Восстановление при запуске выполняет локальные проверки; сеть, открытие браузера или восстановление доступности remote сами по себе не возобновляют приостановленную базу. `paused`, `conflict`, `needs_reconnect`, `initializing` и `syncing` не имеют активного recurring срока.

Успешный sync, conflict complete или initialize сбрасывает счётчик в `0` и публикует `ready`. Явный resume и успешное изменение Git-конфигурации сбрасывают счётчик перед постановкой новой операции в очередь. Первая операционная ошибка после resume начинает новую последовательность с `1`.

### `GET /api/git/status?base=work`

Полный пример четвёртой последовательной операционной ошибки (`200 OK`):

При ошибке `stage` сохраняет последнюю достигнутую стадию, например `fetching` или `pushing`, а не становится `completed`. В следующих примерах ошибка произошла при push.

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "error",
    "operation_id": "11111111111111111111111111111111",
    "stage": "pushing",
    "ahead": 1,
    "behind": 0,
    "consecutive_failures": 4,
    "last_attempt": "2026-09-01T12:00:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": [],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567",
    "error": {"code": "remote_unreachable", "message": "Git remote is unreachable"}
  }]
}
```

Полный пример пятой ошибки, открывшей breaker:

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "paused",
    "operation_id": "22222222222222222222222222222222",
    "stage": "pushing",
    "ahead": 1,
    "behind": 0,
    "consecutive_failures": 5,
    "last_attempt": "2026-09-01T12:15:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": [],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567",
    "error": {"code": "remote_unreachable", "message": "Git remote is unreachable"}
  }]
}
```

Успешный conflict abort также публикует явную паузу, сохраняя предыдущий счётчик, например `2`. Поля `ahead` и `behind` равны `0`, поле `error` отсутствует; интерфейс использует пояснение по умолчанию: «Автоматическая синхронизация остановлена до явного возобновления.»

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/home/user/notes/work",
    "state": "paused",
    "operation_id": "33333333333333333333333333333333",
    "stage": "completed",
    "ahead": 0,
    "behind": 0,
    "consecutive_failures": 2,
    "last_attempt": "2026-09-01T12:20:00Z",
    "last_success": "2026-09-01T11:00:00Z",
    "changed_paths": ["notes/idea.md"],
    "remote_oid": "0123456789abcdef0123456789abcdef01234567"
  }]
}
```

### `POST /api/git/resume?base=work`

Resume принимает только `paused`, включая breaker и паузу после abort. Он получает свежий снимок настроек, атомарно сбрасывает счётчик, долговечно ставит одну обычную sync-операцию в очередь и публикует `syncing`. Resume не меняет конфигурацию, не отменяет конфликт и не обходит `conflict` или `needs_reconnect`. Ошибка записи очереди или queued-статуса восстанавливает точный предыдущий paused-статус.

Ответ `202 Accepted`:

```json
{"operation_id":"44444444444444444444444444444444","status":"queued","deduplicated":false}
```

Повторный resume, пока принятая sync-операция ещё активна, отвечает `202` с тем же `operation_id` и `deduplicated: true`; он не создаёт вторую операцию и не сбрасывает счётчик повторно. `202` означает принятие, а не завершение: клиент следит за `syncing`, затем за терминальным статусом через GET status. Перед resume веб-интерфейс завершает загрузку изображений и сохраняет текущую заметку; ошибка сохранения блокирует запрос.

Resume для базы вне паузы отвечает `409 Conflict`:

```json
{"code":"git_not_paused","message":"Git synchronization is not paused"}
```

Обычный `POST /api/git/sync?base=work` при `paused` отвечает `409 Conflict`. Ошибка `git_paused` возвращается при отказе принять обычный sync-запрос, а не записывается в статус успешного abort:

```json
{"code":"git_paused","message":"Git synchronization is paused"}
```

### Изменения настроек и файлов

| Изменение | Статус и расписание |
|:---|:---|
| Переименование базы с тем же каноническим путём | Сохраняются счётчик, OID, timestamps и срок; меняется имя базы |
| Включение autosync, изменение интервала, шаблона, URL или ветки | Счётчик сбрасывается; очередь initialize, затем расписание по новому статусу |
| `auto_sync: false` через Git-настройки | Счётчик сбрасывается, одна явная initialize-операция; recurring срок удаляется |
| Изменение пути базы | Старый статус и срок удаляются; новый путь получает `needs_reconnect` со счётчиком `0` |
| Отключение Git через `DELETE /api/git/config?base=<name>` | Статус и recurring срок удаляются |
| Забывание базы | Статус и recurring срок удаляются |

Отключение autosync, отключение Git и забывание базы **не удаляют `.git`, каталог базы или пользовательские файлы**. Замена конфигурации применяет те же правила по каноническим путям и новому порядку баз.

## Git: конфликты слияния

Все следующие маршруты находятся под `/api`, требуют завершённой настройки базы и локального origin. Они доступны только когда статус базы равен `conflict`.

Стадии индекса Git фиксированы: `base` — стадия **1**, `local` — стадия **2**, `remote` — стадия **3**. Сервер не выбирает вариант автоматически.

### `GET /api/git/conflicts?base=<name>`

Возвращает текущий снимок merge и не изменяет репозиторий. Параметр `base` обязателен и должен быть указан один раз. Поля `conflicts` и `actions` всегда являются массивами.

Пример текстового конфликта:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "head_oid": "1111111111111111111111111111111111111111",
  "merge_head_oid": "2222222222222222222222222222222222222222",
  "conflicts": [
    {
      "id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "kind": "content",
      "content_kind": "text",
      "path": "notes/idea.md",
      "base": {"path": "notes/idea.md", "oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mode": "100644", "size": 5, "content": "base\n", "preview_truncated": false},
      "local": {"path": "notes/idea.md", "oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mode": "100644", "size": 6, "content": "local\n", "preview_truncated": false},
      "remote": {"path": "notes/idea.md", "oid": "cccccccccccccccccccccccccccccccccccccccc", "mode": "100644", "size": 7, "content": "remote\n", "preview_truncated": false},
      "actions": ["local", "remote", "manual"]
    }
  ],
  "can_complete": false
}
```

Бинарный add/add: бинарные байты не сериализуются, а доступные действия задаёт `actions`.

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "head_oid": "1111111111111111111111111111111111111111",
  "merge_head_oid": "2222222222222222222222222222222222222222",
  "conflicts": [
    {
      "id": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "kind": "add_add",
      "content_kind": "binary",
      "path": "assets/images/logo.png",
      "local": {"path": "assets/images/logo.png", "oid": "dddddddddddddddddddddddddddddddddddddddd", "mode": "100644", "size": 2048, "preview_truncated": false},
      "remote": {"path": "assets/images/logo.png", "oid": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "mode": "100644", "size": 3072, "preview_truncated": false},
      "actions": ["local", "remote", "keep_both"]
    }
  ],
  "can_complete": false
}
```

Полный ответ для modify/delete, где local (стадия 2) сохранён, а remote удалён:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "head_oid": "1111111111111111111111111111111111111111",
  "merge_head_oid": "2222222222222222222222222222222222222222",
  "conflicts": [
    {
      "id": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
      "kind": "modify_delete",
      "content_kind": "text",
      "path": "notes/draft.md",
      "base": {"path": "notes/draft.md", "oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mode": "100644", "size": 5, "content": "base\n", "preview_truncated": false},
      "local": {"path": "notes/draft.md", "oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mode": "100644", "size": 5, "content": "edit\n", "preview_truncated": false},
      "actions": ["local", "manual", "delete"]
    }
  ],
  "can_complete": false
}
```

Полный ответ для rename/delete: `path` — переименованное назначение, `original_path` — удалённый исходный путь.

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "head_oid": "1111111111111111111111111111111111111111",
  "merge_head_oid": "2222222222222222222222222222222222222222",
  "conflicts": [
    {
      "id": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
      "kind": "rename_delete",
      "content_kind": "text",
      "path": "notes/renamed.md",
      "original_path": "notes/draft.md",
      "base": {"path": "notes/renamed.md", "oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "mode": "100644", "size": 5, "content": "base\n", "preview_truncated": false},
      "local": {"path": "notes/renamed.md", "oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mode": "100644", "size": 5, "content": "edit\n", "preview_truncated": false},
      "actions": ["local", "manual", "delete"]
    }
  ],
  "can_complete": false
}
```

`kind` принимает `content`, `add_add`, `modify_delete` или `rename_delete`; `content_kind` — `text` или `binary`. Для валидного UTF-8 текста больше 1 MiB `content` опускается и `preview_truncated` равно `true`.

### `PUT /api/git/conflicts/resolve`

Разрешает ровно один логический конфликт. Обязательны `base`, `operation_id`, `conflict_id`, `path` и `action`. Идентификаторы и OID должны соответствовать текущему снимку; устаревшие значения не выбирают изменённый файл.

Выбор local (стадия 2) или remote (стадия 3) требует соответствующего OID и `result_path`. Для remote в таком же запросе передаются `action: "remote"` и `remote_oid` вместо `local_oid`:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "conflict_id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "path": "notes/idea.md",
  "action": "local",
  "result_path": "notes/idea.md",
  "local_oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}
```

Полный запрос для выбора remote:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "conflict_id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "path": "notes/idea.md",
  "action": "remote",
  "result_path": "notes/idea.md",
  "remote_oid": "cccccccccccccccccccccccccccccccccccccccc"
}
```

Ручное текстовое разрешение требует `result_path` и `content`:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "conflict_id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "path": "notes/idea.md",
  "action": "manual",
  "result_path": "notes/idea.md",
  "content": "# Combined idea\n"
}
```

`manual` разрешён только если он присутствует в `actions`; бинарный конфликт вручную не разрешается.

`keep_both` доступен при наличии обеих сторон для бинарного content-конфликта и конфликтов `add_add`, если указан в `actions`; он требует разных выходных путей и обоих OID:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "conflict_id": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "path": "assets/images/logo.png",
  "action": "keep_both",
  "local_path": "assets/images/logo-local.png",
  "remote_path": "assets/images/logo-remote.png",
  "local_oid": "dddddddddddddddddddddddddddddddddddddddd",
  "remote_oid": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
}
```

`delete` доступен только при наличии в `actions`, не требует полей действия и удаляет все пути логического конфликта (включая исходный и переименованный пути rename/delete). Отсутствующая стадия local/remote означает удаление этой стороной: нельзя выбрать отсутствующий blob; для удаления используется `delete`. Например:

```json
{
  "base": "work",
  "operation_id": "0123456789abcdef0123456789abcdef",
  "conflict_id": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "path": "notes/draft.md",
  "action": "delete"
}
```

Отклоняются абсолютные пути, traversal, компоненты `.git`, пересечения с другим неразрешённым конфликтом и не принадлежащие конфликту существующие назначения. Клиент использует `actions` свежего снимка, а не предполагает доступность действий по расширению файла.

Успешный ответ содержит исходный `resolved_path` и новый полный снимок в `remaining`:

```json
{
  "resolved_path": "notes/idea.md",
  "remaining": {
    "base": "work",
    "operation_id": "0123456789abcdef0123456789abcdef",
    "head_oid": "1111111111111111111111111111111111111111",
    "merge_head_oid": "2222222222222222222222222222222222222222",
    "conflicts": [],
    "can_complete": true
  }
}
```

### `POST /api/git/conflicts/complete?base=<name>`

Запрос без тела, возможен только при `can_complete: true`, когда не осталось неразрешённых конфликтов. Маршрут ставит в очередь долговечную операцию и отвечает `202 Accepted`:

```json
{
  "operation_id": "fedcba9876543210fedcba9876543210",
  "status": "queued",
  "deduplicated": false
}
```

Повторный запрос той же операции возвращает тот же ID с `deduplicated: true`. Выполнение создаёт двухродительский merge commit, перестраивает индекс заметок, пушит именно зафиксированный merge OID, обновляет trusted remote OID и публикует `ready`. `changed_paths` готового статуса — точный diff с исходным local commit.

### `POST /api/git/conflicts/abort?base=<name>`

Запрос без тела ставит в очередь локальную операцию отмены и возвращает ту же самостоятельную схему `202`. Она отменяет слияние, перестраивает восстановленные пути, не выполняет push и публикует `paused`. GET status после успешного abort (без `error`):

```json
{
  "statuses": [{
    "base": "work",
    "repository_path": "/notes/work",
    "state": "paused",
    "operation_id": "fedcba9876543210fedcba9876543210",
    "stage": "completed",
    "ahead": 0,
    "behind": 0,
    "consecutive_failures": 0,
    "changed_paths": ["notes/idea.md", "notes/todo.md"],
    "remote_oid": "2222222222222222222222222222222222222222"
  }]
}
```

После abort `HEAD` и рабочее дерево возвращаются к local commit, unmerged index entries исчезают, а remote и trusted remote OID не меняются. Обычные изменения заметок разрешены, однако `POST /api/git/sync?base=<name>` отвечает `409 git_paused` до явного `POST /api/git/resume?base=<name>`; abort не ставит sync в очередь. Успешная отмена не записывает `git_paused` в поле `error`.

### Ошибки и восстановление

Ошибки имеют форму `{ "code", "message", "field?" }`. Сообщения ошибок не раскрывают пути, OID, URL, команды и диагностику Git. Структурированные пути и OID присутствуют только в предусмотренных полях status/probe/conflicts.

```json
{"code":"git_conflict_stale","message":"Git conflict changed; refresh and try again"}
```

```json
{"code":"git_conflict_unresolved","message":"Git conflict still has unresolved paths"}
```

```json
{"code":"git_paused","message":"Git synchronization is paused"}
```

```json
{"code":"git_recovery_required","message":"Git repository requires recovery"}
```

`git_conflict_not_found` отвечает `404`; `git_conflict_unsupported` — `422` и может содержать `field`; `git_merge_not_in_progress`, `git_recovery_required`, `repository_locked` и `git_conflict_pending` отвечают `409`. Небезопасный restart, неоднозначное внешнее изменение или `index.lock` остаются mutation-blocked и возвращают recovery error вместо автоматического выбора стороны. Восстановление выполняет только локальные проверки: частично разрешённые стадии 1/2/3 сохраняются, а Git state/lock файлы не удаляются приложением.

## Машинные коды ошибок

Клиент принимает решения по `code`, а не тексту `message`. Ошибка HTTP — JSON `APIError`; probe может вернуть её в `blocking_error` с HTTP 200, а фоновая операция — в `status.error` с HTTP 200 при чтении статуса. Эти каналы не следует смешивать.

```json
{"code":"missing_field","message":"Missing required field","field":"base"}
```

### Git-коды (`internal/git/errors.go`) и конфликтные sentinel-ошибки

В колонке HTTP указан результат **прямого отказа обработчика** согласно `writeServiceError`. Некоторые коды предназначены для фонового статуса/probe: если такая `SafeError` дойдёт непосредственно до HTTP-обработчика без отдельного mapping, он возвращает `500 internal_error`, а не этот код.

| Код | HTTP при прямом отказе | Значение |
|:---|:---|:---|
| `git_unavailable` | 503 | Git executable недоступен |
| `git_version_unsupported` | 422 | Нужен Git 2.28+ |
| `auth_failed` | 401 | Не удалось аутентифицироваться |
| `remote_unreachable` | 502 | Remote недоступен |
| `identity_missing` | 422 | Не настроены имя и email автора |
| `invalid_branch` | 422 | Неверная или отсутствующая выбранная ветка |
| `repository_root_mismatch` | 409 | Корень репозитория не совпадает с корнем базы |
| `repository_locked` | 409 | Lock или ожидающая Git-операция |
| `git_command_failed` | 500 → `internal_error` | Безопасный общий отказ Git |
| `git_timeout` | 504 | Истёк timeout |
| `git_canceled` | 408 | Операция отменена |
| `not_a_git_repository` | 500 → `internal_error` | Каталог не является Git-репозиторием |
| `origin_mismatch` | 409 | Origin отличается от настроенного |
| `branch_deleted` | 409 | Удалённая ветка исчезла |
| `remote_history_rewritten` | 409 | Remote history переписана |
| `push_rejected` | 409 | Push отклонён |
| `git_conflict` | 500 → `internal_error` | Фоновое слияние обнаружило конфликт |
| `needs_reconnect` | 409 | Требуется переподключение |
| `git_confirmation_required` | 500 → `internal_error` | Требуемое изменение не подтверждено для initialize |
| `operation_interrupted` | 500 → `internal_error` | Работа прервана, например при shutdown |
| `backup_mismatch` | 500 → `internal_error` | Backup не соответствует ожидаемому снимку |
| `git_conflict_not_found` | 404 | Конфликт не найден |
| `git_conflict_stale` | 409 | Снимок конфликта устарел; обновить список |
| `git_conflict_unresolved` | 409 | Ещё остались неразрешённые пути |
| `git_conflict_unsupported` | 422 | Неподдерживаемый тип/действие/назначение; возможен `field` |
| `git_merge_not_in_progress` | 409 | Нет текущего слияния |
| `git_recovery_required` | 409 | Требуется восстановление; неоднозначное состояние также использует этот код |
| `git_paused` | 409 | Обычный sync отклонён из-за паузы |
| `git_not_paused` | 409 | Resume вызван вне паузы |

### Ошибки сервисов и HTTP-проверок

| HTTP | Коды | Значение |
|:---|:---|:---|
| 400 | `bad_json`, `bad_query`, `missing_field`, `invalid_request`, `invalid_mode` | Неверный запрос, параметры или режим |
| 400 | `invalid_path`, `missing_file`, `file_too_large` | Недопустимый путь заметки или multipart-загрузка |
| 403 | `forbidden_origin` | Не разрешён локальный origin/Host |
| 404 | `note_not_found`, `file_not_found`, `base_not_found` | Ресурс не найден |
| 405 | `method_not_allowed` | Неподдерживаемый метод; см. `Allow` |
| 409 | `note_changed` | Ревизия файла изменилась; требуется свежий GET и подтверждённое сохранение |
| 409 | `note_conflict`, `base_name_conflict`, `base_path_conflict` | Имя или путь уже заняты |
| 409 | `git_conflict_pending` | Обычная мутация заблокирована конфликтным репозиторием |
| 409 | `git_repository_in_use` | Один канонический репозиторий уже занят другой Git-базой |
| 409 | `setup_already_completed`, `setup_cannot_reopen` | Нельзя повторить или открыть заново первичную настройку |
| 409 | `runtime_path_changed` | Путь активной базы изменился вне ожидаемого состояния |
| 409 | `active_base`, `last_base` | Нельзя забыть активную или последнюю базу |
| 422 | `invalid_config`, `invalid_base_name`, `invalid_base_path` | Некорректные настройки базы |
| 422 | `invalid_git_url`, `invalid_branch`, `invalid_auto_sync_interval`, `invalid_commit_template` | Некорректные Git-настройки |
| 428 | `setup_required` | Первичная настройка не завершена |
| 500 | `internal_error`, `rollback_failed`, `directory_picker_failed` | Безопасная внутренняя ошибка или ошибка отката/диалога |
| 501 | `directory_picker_unavailable` | Системный выбор каталога недоступен |
| 503 | `git_manager_closed` | Менеджер операций остановлен |

API никогда не сериализует raw stdout/stderr, диагностические сообщения Git, argv/команды, credentials, приватную причину (`cause`) или цепочку внутренних ошибок. Безопасное сообщение не является командой для исполнения; поля ошибок не содержат секретов или произвольных деталей underlying failure.

{% endraw %}
