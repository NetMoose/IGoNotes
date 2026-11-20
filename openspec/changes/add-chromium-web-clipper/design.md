# Design

## Context

Мотивация и продуктовый объём — в `proposal.md`; проверяемое поведение — в delta specs change.

Наблюдаемое состояние проекта:
- `internal/handlers/routes.go` оборачивает API в `RequireLocalOrigin`; `origin_guard.go` отклоняет extension Origin и cross-site fetch metadata. Расширение не может использовать эти маршруты напрямую.
- Создание заметки и сохранение содержимого — разные запросы; `SaveAsset` пишет в активную базу. Последовательность этих запросов не обеспечивает адресный импорт и восстановление после потери ответа.
- `NoteService` держит `os.Root`, `baseMu` и индекс только активной базы. Его `MutateActiveFilesystem` намеренно обходит conflict gate для Git resolver: использовать его как обычный импорт нельзя.
- `BaseOperationCoordinator` задаёт порядок `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`, блокирует lifecycle/Git и предоставляет lock-free проверку конфликтов по пути/идентичности.
- SQLite и Git-журнал находятся вне баз, миграции последовательны в `internal/repository/db.go`. UI настроек — `web/src/lib/settings/SettingsWorkspace.svelte`; frontend использует JavaScript, Svelte 5/Vite и Vitest. `Makefile` пока собирает только `web/` и Go.
- `Editor.svelte:renderMarkdown` передаёт относительный image src непосредственно в `/api/raw`, то есть считает его base-root путём. Кроме того, frontmatter вставляется в `<pre>` без HTML escaping. Оба места требуют точечной адаптации для внешнего текста и переносимых вложенных заметок.

Официальная документация Chrome через Context7 подтверждает: cross-origin fetch выполняется в extension page/worker с host permissions; content script остаётся связан с origin страницы. Часть запросов документации Readability/жизненного цикла завершилась timeout: это не считается подтверждением конкретных библиотечных API. Выбранные зависимости проверяются на зафиксированных версиях при реализации.

## Goals / Non-Goals

**Goals:**
- Независимые границы: extraction/conversion, draft persistence, image acquisition, transport, server authorization, base-scoped publication.
- Сохранять пользовательскую работу прежде сетевых операций и различать неподтверждённый результат от точно отклонённого запроса.
- Не расширять полномочия существующего API и не переключать runtime ради импорта в другую базу.
- Обеспечить восстановление после остановки процесса, не обещая единой атомарной транзакции SQLite и нескольких файлов.

**Non-Goals:**
- Байтовая копия сайта, обход paywall/CSP/аутентификации, гарантированное извлечение любой страницы.
- Бесконечная работа service worker, необязательный remote-code runtime, глобальное переустройство индекса заметок.
- Доверенный HTML-preview: первая версия использует редактор Markdown как текст и список изображений, без рендеринга HTML страницы.

## Decisions

### 1. Transport: dedicated loopback HTTP API

Выбран отдельный `/api/clipper/v1/` с Bearer-токеном. Native Messaging требует установки/регистрации native host на каждой ОС; custom URL protocol неудобен для больших изображений, ответов и повторов. HTTP использует уже работающий процесс IGoNotes и настраиваемый loopback-порт.

```text
HTTP(S) tab
    |
    v
isolated capture --> draft editor tab --> IndexedDB (text + blobs)
                             |
                             v
                    extension transport
                             |
                    Bearer / loopback HTTP
                             |
                             v
                   Clipper routes + auth
                             |
                       import service
                             |
            +----------------+----------------+
            v                                 v
       SQLite journal                   selected base root
       outside bases                    note.md + assets/images/
```

Первая поддерживаемая среда — desktop Chrome/Chromium 120+ с MV3; документируются реально проверенные версии ОС/браузера. Первая поставка — Load unpacked из отдельного `dist/`, без обещания Chrome Web Store approval. Автозапуск IGoNotes не нужен.

### 2. Authorization without weakening existing routes

Новые same-origin management-маршруты `GET/POST /api/clipper/connections` и `DELETE /api/clipper/connections?id=...` используют существующие local-origin/setup guards. UI добавляет вкладку «Web Clipper» с именованными подключениями, созданием и отзывом. POST возвращает случайный 256-bit секрет один раз; SQLite хранит ID, label, SHA-256 секрета, timestamps и revoked state. Сравнение digest — constant-time. Секрет не помещается в `Config`, URL или логи; ответы выдачи имеют `Cache-Control: no-store`.

В расширении endpoint/token хранятся в `chrome.storage.local`, не `sync`; доступ ограничивается trusted extension contexts. Content script получает только команду capture и возвращает данные. Нет `externally_connectable`, публичного message API или произвольного fetch по URL из сообщения страницы. Проверяются sender и схема сообщений.

Clipper guard отдельно проверяет loopback Host, Bearer и, если Origin присутствует, точную форму `chrome-extension://<valid-id>`; `null`, HTTP(S)-Origin и неоднозначные заголовки отклоняются. Отсутствие Origin допустимо только с валидным Bearer на loopback. Не полагаться на `Sec-Fetch-Site` как на авторизацию расширения: его допустимое значение для extension fetch проверяется browser smoke-тестом. CORS отражает только валидный extension Origin, с `Vary: Origin`, без cookies/credentials и wildcard. Preflight проверяет Host, Origin, метод и headers, но не требует Bearer, который браузер не передаёт в OPTIONS; он не выдаёт данные. Основной запрос всегда требует Bearer. Существующий guard не меняется.

Токен — полномочие на ограниченное чтение назначений/создание новых клипов, не на чтение или редактирование заметок. Отзыв блокирует новые запросы; уже принятый и публикуемый импорт может завершиться. Изменённая установка/профиль расширения требует повторного ввода секрета.

### 3. Versioned external contract

| Route | Meaning |
|---|---|
| `GET /api/clipper/v1/info` | `protocol_version: 1`, app version, supported image types and limits |
| `GET /api/clipper/v1/bases` | names of configured bases, no absolute paths or Git config |
| `GET /api/clipper/v1/folders?base=...` | existing index-visible folders including root `""`, `destination_revision` |
| `POST /api/clipper/v1/imports` | bounded multipart: JSON `manifest` + binary parts keyed by asset ID |
| `GET /api/clipper/v1/imports?id=...` | `pending`, `succeeded`, `rejected` or `recovery_required` with original result |

Все маршруты требуют setup и Bearer. При неизвестном протоколе клиент прекращает отправку. `info` сообщает лимиты, чтобы UI отказал до загрузки. Начальные пределы: Markdown 5 MiB UTF-8; 100 изображений; одно изображение 10 MiB; сумма байтов изображений 50 MiB; всё multipart-тело 64 MiB. Принимаются статические PNG/JPEG/WebP и GIF, включая анимированные, с проверкой сигнатуры, согласованного MIME и полного декодирования. SVG, PDF, APNG и animated WebP не принимаются. Ограничения новых маршрутов не меняют существующий 10 MiB body limit `/api/assets`.

Пользователь согласовал явные ограничения декодирования: `image_pixels = 40000000` на изображение и отдельный кадр, `gif_frames = 100`, `gif_total_pixels = 200000000` суммарно по кадрам одного GIF. GIF logical canvas также ограничен 40 МП. Эти поля добавляются в `limits` ответа `info`. До выделения пиксельных буферов выполняется ограниченный структурный preflight: размеры всех кадров и их сумма проверяются с защитой от overflow; признаки APNG/animated WebP дают `415 clipper_unsupported_image`, превышение размеров/кадров — `413 clipper_too_large`. После preflight полностью декодируются все поддерживаемые данные (включая все GIF-кадры); повреждение/обрезанный payload не считается допустимым только из-за верной сигнатуры/digest. Полные декодирования сериализованы process-wide с отменяемым ожиданием, чтобы параллельные upload не умножали максимальное выделение памяти; lifecycle/SQLite locks при этом не удерживаются. Исходные байты сохраняются без перекодирования. Для неподдерживаемых или слишком больших изображений остаётся предусмотренный явный external fallback.

Manifest: `import_id` (random UUID), `base`, `destination_revision`, `parent`, `filename`, `markdown`, `source_url`, `captured_at`, `assets: [{id, sha256, media_type, size}]`, `external_images: [{url, reason}]`. Имя файла — один безопасный компонент с `.md`, предложенный из заголовка; абсолютные пути, `.git`, Windows reserved names/characters, control characters и path separators отклоняются. Папка передаётся отдельно, уже должна существовать. JSON и multipart parts валидируются строго: без повторных/необъявленных asset IDs, превышения размеров и trailing мусора. Сервер не выполняет fetch URL.

Успех `201` для нового и `200` для повторного импорта: `import_id`, `base`, `path`, `revision`, `warnings`. Новая публикация синхронна; незавершённый одинаковый запрос может получить `202 clipper_import_pending` и затем проверить GET. Ошибки используют текущую форму APIError: `401 clipper_unauthorized`, `428 setup_required`, `400 clipper_invalid_request`, `413 clipper_too_large`, `415 clipper_unsupported_image`, `404 clipper_destination_not_found`, `409 clipper_destination_changed`, `409 note_conflict`, `409 git_conflict_pending`, `409 clipper_request_changed`, `409 clipper_recovery_required`. Серверные IO/индекс-ошибки не выдаются за подтверждённый отказ без проверки журнала.

Ревизия назначения — непрозрачный случайный handle снимка имени/канонического пути/идентичности открытого корня. Реестр снимков живёт в процессе; settings lifecycle инвалидирует снимки при rename/path change/forget, повторная проверка root identity ловит замену каталога. После рестарта незавершённый черновик требует обновления/подтверждения назначения. Уже завершённый повтор ищется по import_id/digest ДО проверки устаревшей ревизии, поэтому потерянный успешный ответ восстанавливается и после рестарта/переименования базы. GET результата явно показывает историческое назначение, не обещая, что файл впоследствии не изменён.

### 4. Extraction and editable Markdown

Самостоятельный `extensions/webclipper/` использует JavaScript ESM, Vite, Vitest и небольшой Svelte UI, согласованные с проектом; lockfile и Node.js 24. Worker, action popup, isolated content script и extension editor tab собираются как отдельные MV3 entry points, без remote scripts/eval. CSP — локальные script resources; необходимые GFM-зависимости поставляются bundled, с сохранением license notices.

Action popup выбирает «Статья»/«Выделение» и запускает `scripting.executeScript` в MAIN FRAME isolated world, пока исходная вкладка доступна. Article extraction — Mozilla Readability на клоне документа; selection — клонирование DOM Range с точными границами в инертной копии документа, не эвристика всей статьи. Live Range не клонируется: даже отсоединённые img в его ownerDocument могут начать сетевую загрузку до Prepare. Режим не подменяется автоматически. Readability может вернуть отсутствие статьи — это явная ошибка с предложением выделить текст. Нет захвата cross-origin iframe, shadow DOM и PDF в первой версии.

После извлечения — allowlist нормализация DOM, удаление script/style/form/iframe/event attributes и опасных URL, разрешение относительных адресов с учётом `document.baseURI`, преобразование Turndown + GFM rules. Не считать Readability санитайзером. Пользовательские HTML-вставки не рендерятся в extension UI. Локальные тестовые страницы проверяют headings/lists/quotes/code/tables, lazy-loaded изображения, `srcset/currentSrc`, Unicode и selection. Конвертер не переносит весь HTML при неизвестном элементе, а сохраняет безопасный текст.

Черновик содержит title, Markdown body, source URL и captured_at в UTC. Итоговый файл получает сериализованный безопасным способом YAML frontmatter (`title`, `source`, `clipped_at`) и Markdown body; метаданные показываются отдельно. Не интерполировать title/URL напрямую в YAML. Filename редактируется отдельно от title. Перед отправкой состав изображений вычисляется из последнего текста, а не устаревшего DOM.

### 5. Permissions and image acquisition

Базовые permissions — `activeTab`, `scripting`, `storage`. Loopback host permissions ограничены поддерживаемыми `http://127.0.0.1/*`, `http://localhost/*`, `http://[::1]/*`; фактический endpoint валидируется с портом, без credentials/path/query/fragment, без redirect при отправке токена. Endpoint по умолчанию `http://127.0.0.1:8080`. Match patterns не ограничивают порт: это объясняется в документации. Совместимость IPv6 pattern проверяется сборкой/браузером, недоступный вариант не показывается как рабочий.

Для изображений объявляются optional HTTP(S) host patterns; разрешения запрашиваются по origin только после явного действия в editor. Не запрашивать обязательный `<all_urls>`. Fetch идёт из extension context с timeout, `credentials: omit` и `redirect: error`: redirect получает отдельную ошибку и допускает ручную замену URL/повторную подготовку после нужного разрешения. Localhost/private IP literals и не-HTTP(S) URL не загружаются как картинки страницы; это не обещание защиты от всех DNS rebinding вариантов. Не использовать content script как универсальный сетевой прокси. Data-image PNG/JPEG/GIF/WebP можно декодировать локально в пределах тех же лимитов; blob URL и изображения, требующие сессионных cookies, получают понятный статус недоступности. Опасные/неподдерживаемые URL не становятся активными внешними ссылками.

Изображения скачиваются с небольшим параллелизмом (3), дедуплицируются по нормализованному URL и SHA-256 байтов. Успешный Blob записывается в IndexedDB до показа «сохранено». Ошибки/отказ разрешения оставляют первоначальный URL и статус. Перед импортом пользователь должен исправить ошибки или подтвердить список оставляемых HTTP(S)-ссылок. Добавленные при редактировании изображения проходят ту же подготовку; удалённые больше не включаются в manifest.

Финальное имя локального изображения детерминировано: `clip-<import_id>-<sha256>.<verified-ext>` в `assets/images/`. Extension переписывает ТОЛЬКО image destinations через Markdown parser/token ranges, не глобальный replace текста; ссылки в code blocks и обычный текст неизменны. Путь считается от папки заметки (`../../assets/images/...` для `research/articles/`). Сервер вычисляет те же имена из проверенных байтов; запрос не управляет путями attachments. Исходный редактируемый Markdown и map URL -> Blob сохраняются, upload snapshot строится отдельно. При существующем детерминированном файле разрешено только подтверждённое журналом повторное использование того же собственного импорта; чужой файл не перезаписывается.

В `Editor.svelte` выделить тестируемый resolver image paths: стандартные `./`/`../` пути нормализуются относительно каталога открытой заметки и только после проверки границы корня переводятся в `/api/raw?path=...`. Не выполнять filesystem fallback по существованию разных файлов. Сохранить прежнюю трактовку `assets/images/...`, wiki-links и готовых `/api/raw` ссылок; внешние HTTP(S)/data изображения обрабатываются как прежде. Отдельно экранировать frontmatter при выводе как текст в `<pre>`, чтобы символы title/source не становились HTML. Это адресная интеграция клипов, не полная переработка Markdown renderer.

### 6. Durable drafts and delivery state machine

IndexedDB хранит `drafts`, `assets`, `attempts`; chrome.storage.local — только настройки/секрет. Текст, asset metadata и Blob обновляются транзакционно, UI показывает индикатор сохранения. Предлагаемые application caps: 50 черновиков и 250 MiB изображений суммарно; меньшая квота браузера обрабатывается как storage error. Нет молчаливой очистки, sync/cloud upload или обещания сохранности после удаления расширения/профиля. «Скачать Markdown» служит выходом при нехватке места; не выдаётся за экспорт всех картинок.

```text
capturing --> draft --> preparing --> ready --> sending --> succeeded
                ^          |            |          |
                +----------+------------+          v
                   visible error               uncertain
                                                   |
                                      query/replay same attempt
```

Editor — отдельная вкладка расширения, а не долгоживущий popup. Worker нужен для capture coordination и коротких сообщений; сетевые этапы/картинки выполняет editor с сохранёнными checkpoints. После закрытия окна или остановки worker следующая загрузка UI восстанавливает interrupted state; долгоживущая background queue не требуется.

Перед первой отправкой immutable attempt (`import_id`, canonical manifest, blobs/digests, endpoint) сохраняется в IndexedDB. Digest — SHA-256 канонически сериализованных семантических полей manifest и отсортированного списка digest/size/type assets; случайные multipart boundaries не учитываются. Сервер вычисляет самостоятельно. Секрет не входит в digest, поэтому повторное подключение допустимо. Смена сервера не переносит uncertain attempt автоматически: пользователь должен разрешить результат на исходном сервере или явно создать копию с предупреждением о возможном дубликате.

При network timeout/5xx/закрытии окна попытка считается uncertain: сначала GET status или точный replay, без нового import_id. Редактировать или менять назначение uncertain attempt нельзя до разрешения результата; допускается скачать Markdown. Точно rejected attempt можно исправить и отправить с новым ID. На успех показываются база/path/warnings, затем очищаются draft blobs. Отказ авторизации сохраняет всё и предлагает переподключение. Кэш назначений полезен офлайн, но проверяется перед новой отправкой; автоматической доставки при восстановлении связи нет.

### 7. Base-scoped publication and journal

Пользователь подтвердил глобальную защиту служебных путей вместо временного резервирования только для Clipper. Неизменяемый layout включает каталог конфигурации, SQLite `metadata.db` с его служебными файлами и отдельный приватный каталог `<appDataDir>/clipper-staging/`. Каталог базы не может содержать эти пути, совпадать с приватным каталогом либо находиться внутри него. Не запрещать весь `appDataDir`: обычная база `<appDataDir>/bases/default` остаётся разрешённой.

Одна path policy применяется до публикации настроек и записи данных на startup (проверяются все настроенные базы, включая неактивные), setup, add/update/replace config и переключении базы. Проверяются канонические пути, aliases/symlink и существующие предки ещё не созданных каталогов; `filepath.Rel`/native identity предпочтительнее строкового prefix. Политика хранит неизменяемые служебные назначения и проверяется под обычными lifecycle locks, поэтому конфигурация не может объявить staging частью базы во время upload. Сетевое чтение не удерживает coordinator. API использует существующий `422 invalid_base_path`; startup объясняет несовместимость конфигурации и прекращается до открытия сервера/записи в базу. Несовместимые пользовательские данные не перемещаются и не удаляются; инструкция исправления входит в user docs. Staging handler использует только зарезервированный приватный каталог с проверенной идентичностью, а не произвольный `appDataDir`.

Добавить `ClipImportService`, ограниченный snapshot helper в SettingsService и отдельные файловые primitives, а не выполнять `/api/bases/switch`. Folder enumeration сканирует выбранный pinned `os.Root` без записи в глобальный notes index. Для активной базы использовать её runtime identity; для неактивной — scoped root, закрываемый после операции.

Пользователь подтвердил ограничение назначения папками, видимыми в дереве. Discovery и проверка новой публикации используют те же правила исключения каталогов, что `scanNotes`: любой компонент, начинающийся с `.`, и `assets` без учёта регистра исключает весь подкаталог на любой глубине. Корень `""` разрешён. Такой `parent` отклоняется с `400 clipper_invalid_request` до claim и записи файлов, даже если он передан вручную; правила индекса не расширяются. Хранение вложений в `assets/images/` остаётся отдельной серверной операцией. Регрессии должны доказывать отсутствие claim/записей для исключённых назначений и видимость успешного импорта в обычную вложенную папку.

Network reading, проверка размеров/hash и staging выполняются ДО coordinator/write locks в приватном каталоге вне баз рядом с metadata.db. Начальная проверка auth/destination предшествует приёму больших данных; после staging полномочия и назначение проверяются снова. Публикация использует порядок `coordinator -> settings snapshot lock -> NoteService.baseMu -> repository/SQLite`; ни один callback не вызывает верхний сервис повторно. Даже неактивный импорт берёт runtime lock для согласованности active/not-active проверки и индекса. Выбранный корень проверяется conflict gate по canonical path/identity; resolver-only `MutateActiveFilesystem` не используется. Обычные note mutations сериализуются через `baseMu`. Coordinator исключает публикацию во время Git/lifecycle; fetch/push не выполняются импортом и сетевых операций под locks нет.

Новые SQLite-таблицы:
- `clipper_connections`: digest секрета и метаданные полномочий.
- `clip_imports`: import_id PK, request_digest, исходное назначение/identity, note path/hash, asset ownership/hash manifest, durable state, result JSON и ошибки. Успешные compact receipts не удаляются автоматически; Blob-стейджинг очищается после устойчивого завершения.

Journal states: `prepared -> publishing -> succeeded`, либо `rejected`/`recovery_required`. До первой записи в базу журнал содержит все планируемые конечные пути, hashes, ownership tokens и staging references. Публикация не является атомарной группой файлов:
1. Полностью провалидировать manifest/blobs и актуальный корень, записать durable `prepared`.
2. Под root confinement создать уникальные временные файлы в нужных каталогах, fsync/close; стадии и ownership фиксировать до шагов, требующих recovery.
3. Опубликовать изображения no-replace, затем полностью записанную заметку no-replace последней. Использовать существующие платформенные no-replace patterns; не полагаться на обычный rename поверх существующего файла.
4. Обновить индекс, если выбранная база активна; неактивная будет просканирована при штатном переключении. Зафиксировать результат/revision как `succeeded`, затем ответить.

Rollback удаляет только подтверждённо принадлежащие операции файлы, чьи identity/hash не изменены. Пустые служебные папки не удаляются рекурсивно. Сбой после публикации заметки или ошибка индекса оставляет pending/recovery state, а не разрешает новый импорт под другим ID. Startup recovery выполняется после локального Git recovery, но до GitManager.Start и нового HTTP-трафика. Новые публикации в затронутый корень блокируются, пока результат не определён. Автоматический rollback разрешён только в исходной критической секции либо при startup до допуска Git; позднее восстановление после освобождения coordinator выполняет только roll-forward, без удаления опубликованных файлов. Если Git recovery сообщает конфликт, importer не меняет worktree и отмечает recovery_required. Полностью совпадающие опубликованные файлы позволяют завершить индекс/receipt; неполная публикация без внешних изменений дозавершается из staging либо безопасно отменяется до допуска Git. Несовпадение identity/hash требует ручного разбора, не удаления пользовательского файла.

Same-origin `GET /api/clipper/imports` показывает незавершённые операции в настройках. Для recovery_required пользователь после самостоятельного сохранения/разбора файлов может явно подтвердить `POST /api/clipper/imports/abandon?id=...`: операция становится terminal rejected с кодом `clipper_import_abandoned`, файлы не удаляются, расширение сохраняет черновик и показывает возможность уже существующей заметки. Такой ID больше не публикуется; новый импорт требует нового имени/ID и явного действия пользователя. Этот административный маршрут не доступен через Bearer Clipper.

Повтор завершённого import_id/digest возвращает receipt, не проверяя текущую ревизию заметки и не восстанавливая удалённые позже файлы. Одинаковый ID с другим digest всегда конфликтует; конкурирующие одинаковые запросы объединяются журналом. Результат доступен после смены токена в той же установке; это не доступ к содержимому заметки. Повтор точно rejected запроса возвращает прежнюю ошибку; исправленный импорт использует новый ID.

### 8. Verification and build integration

- Browserless unit tests: extraction fixtures, GFM conversion, image-only token rewriting, filename/YAML encoding, canonical digest, IndexedDB transactions/quota/restart и delivery transitions.
- Go: route security, migrations/repository, root confinement, inactive destination without switch, collision, digest replay across restart/token rotation, failure injection на каждом publication boundary, active index и lock/Git races.
- Headed real Chromium smoke с Load unpacked и локальным HTTP fixture: capture+selection, cross-origin CDN permissions, loopback origin/fetch metadata/preflight, non-default port, закрытие editor, restart, offline server, image partial failure и nested image rendering. Автоматизируемая часть интегрируется в CI; browser permission prompts дополнительно проходят ручной чек-лист Chrome и Chromium. Loopback Deny/Allow проверяется только при фактическом появлении диалога; если диалог отсутствует, результат — NOT PRESENT, а успешность подключения фиксируется отдельно. Это уточнение приёмки согласовано пользователем 2026-11-20 и не меняет проверку optional CDN permissions.
- Отдельные `npm --prefix extensions/webclipper ci` и `run verify`; make targets/CI добавляются без включения расширения в Go embed. Все frontend-команды после проверки Node.js 24.
- Перед merge — все существующие команды из AGENTS.md, новые extension checks и docs mirror test. Планирование не запускает эти implementation checks.

## Risks / Trade-offs

- [Readability не извлекает часть сайтов] -> явная ошибка и режим выделения; тестировать реальные структуры на локальных fixtures, не обещать полную совместимость Obsidian.
- [Изображения требуют cookies, blob URL или недоступны офлайн] -> сохранять полученные байты заранее, показывать ошибки и явно подтверждать внешние ссылки; это не обход аутентификации.
- [Browser quota / удаление расширения] -> отсутствие автоудаления черновиков, видимый persistence status и Markdown download; локальная копия не равна резервному хранению профиля.
- [MV3, host permissions и политики local network access меняются] -> real-browser smoke для поддерживаемых сборок; отказы разрешений показываются как connectivity errors с сохранённым draft.
- [Несколько файлов + SQLite не образуют атомарную транзакцию] -> durable journal, note-last publish, no-replace и recovery с проверкой ownership; повтор не маскирует неопределённость.
- [Черновик пережил рестарт сервера или смену базы] -> новая ревизия требует подтверждения назначения; receipt уже завершённого импорта остаётся доступен без новой записи.
- [Новые файлы не появляются мгновенно в уже открытом дереве UI] -> следующий штатный запрос дерева видит импорт; server-push и автоматическое открытие клипа не входят в первую версию.
- [Объём включает extension, UI и файловый backend] -> задачи идут вертикальными проверяемыми этапами; первый сквозной сценарий — один текстовый клип, затем изображения и восстановление, без исключения их из итоговой приёмки.

## Migration Plan

1. Добавить следующую последовательную SQLite-миграцию; не менять существующие заметки/конфигурацию и не хранить секреты внутри баз. Подключение отключено фактически до выдачи первого токена.
2. Поставить backend, UI настройки и recovery до распространения расширения; версия протокола не зависит от версии npm-пакета.
3. Собирать самостоятельный MV3 `dist/`/zip и документировать Load unpacked, подключение, лимиты, частично автономные клипы и ручную отправку.
4. Обновить пользовательскую/API/developer документацию и зеркала; выполнить полную приёмку, включая existing Git flow.
5. Rollback функции: отозвать токены и удалить/отключить расширение после экспорта нужных черновиков. Markdown и изображения остаются обычными файлами. Не выполнять down-migration и не запускать старый бинарник поверх неизвестной ему schema version; для отката бинарника использовать предварительную резервную копию metadata DB либо forward-compatible исправление. До отката завершить или разобрать pending import journal.
