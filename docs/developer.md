# Руководство разработчика IGoNotes

## Технологии

- Go 1.26+ и маршрутизация на `net/http`.
- SQLite через чистую Go-реализацию `modernc.org/sqlite` для метаданных.
- Svelte 5, Vite 8, Tailwind CSS 4, CodeMirror 6 и `marked` для frontend.
- Node.js 24 -- поддерживаемая среда выполнения frontend toolchain, включая Vitest.

Git-синхронизация реализована через системный Git; пакет `go-git` в текущие зависимости не входит.

## Структура проекта

- `cmd/api/`: точка входа, CLI, запуск локального сервера и регистрация системных маршрутов.
- `internal/handlers/`: REST-обработчики, guards и SPA handler.
- `internal/appinfo/`: название приложения и версия запущенной сборки.
- `internal/service/`: настройка, управление базами и заметками, синхронизация файловой системы.
- `internal/repository/`: SQLite-репозиторий метаданных.
- `internal/model/`: API, конфигурационные и доменные структуры.
- `internal/git/`: runner системного Git, porcelain, операции одного репозитория, конфликты и локальное восстановление.
- `web/src/`: Svelte-приложение, компоненты, API wrappers и Vitest-тесты.
- `web/dist/`: результат сборки Vite.
- `web/embed.go`: пакет `web` встраивает `web/dist/` директивой `//go:embed all:dist` и предоставляет `fs.FS` серверу.

## Запуск и сборка

Для разработки frontend:

```bash
cd web
npm install
npm run dev
```

Frontend должен быть собран перед отдельным запуском Go-приложения, потому что пакет `web` встраивает `dist`:

```bash
cd web
npm install
npm run build
cd ..
go run ./cmd/api --port 8080
```

Полная сборка создает `builds/igonotes`:

```bash
make all
```

### Версия сборки

`internal/appinfo.Version` по умолчанию равна `dev`. Release workflow передаёт полный тег в каждую платформенную сборку через `-ldflags="-s -w -X IGoNotes/internal/appinfo.Version=${VERSION}"`, где `VERSION` — `github.ref_name`. Для ручной сборки с версией сначала соберите frontend, затем выполните, например:

```bash
go build -trimpath -ldflags="-X IGoNotes/internal/appinfo.Version=v0.0.1-alpha.6" -o builds/igonotes ./cmd/api
```

`GET /api/info` отдаёт `app_name` и `app_version` вместе с `base_path`. Вкладка «О программе» получает версию от бэкенда; `dev` отображается как «Сборка разработки».

## Неизменяемая private-storage policy

`cmd/api/main.go` строит один `service.PrivateStoragePolicy` из resolved config directory, SQLite path и `<appDataDir>/clipper-staging`. Полный layout передаётся в `NewConfigService(configPath, policy)` и через `PrivateStoragePolicy()` наследуется сервисом настроек. Обычный `NewConfigService(configPath)` всегда резервирует хотя бы собственный каталог конфигурации; `ResolveStartupBase` дополняет такой store полным layout до его публикации другим сервисам. Pathless `ConfigStore` fakes могут не предоставлять policy; это совместимость тестов, а не путь запуска production.

`ResolveStartupBase` проверяет все `Base.Path`, включая неактивные, до `InitDB`, runtime/index writes и до создания default/config при первом запуске. `BaseDir` — контейнер и не является базой. Конструкторы settings проверяют snapshot до миграции setup. Setup/add проверяются до `Mkdir`, а изменения конфигурации и switch — под `coordinator -> SettingsService.mu` до persistence/runtime transaction; общая проверка предшествует записи Git statuses. Дополнительно Clipper destination проверяется заново при использовании пути. Symlink ancestors разрешаются заново, включая отсутствующий хвост пути; containment использует `filepath.Rel` с Windows case handling и native identity, а не строковый prefix. Не резервируется весь `appDataDir`: `~/.igonotes/bases/default` допустим.

HTTP text import использует `SettingsService.OpenPrivateStagingRoot() (*os.Root, error)`: под короткими lifecycle locks проверяет актуальные базы, создаёт только выделенный staging (0700), открывает и проверяет pinned root identity. Владение возвращённым handle и `Close` — у вызывающего кода; временные файлы создаются root-relative, сетевое чтение выполняется после освобождения locks. Не использовать весь `appDataDir` как временный каталог. `~/.igonotes/clipper-staging/` зарезервирован постоянно, независимо от наличия upload; request directory с wire body и assets удаляется после возврата importer на любом исходе. Внешняя злонамеренная подмена filesystem не сериализуется coordinator и не входит в гарантию; API lifecycle не может объявить staging частью базы во время upload.

При несовместимой существующей конфигурации startup сообщает имя базы и требует вручную исправить `config.json`; никаких автоматических moves/deletes. Остановите приложение, сохраните backup и укажите отдельные фактические каталоги заметок во всех несовместимых `bases[].path`; вынесите `--config` из базы при необходимости (подробности в user docs). API использует прежний безопасный `422 invalid_base_path`/`FieldError`, без абсолютных служебных путей. Focused regression: `go test ./internal/service ./internal/handlers ./cmd/api -run PrivateStorage -count=1`; зеркала документации проверяет `go test ./internal/docs -count=1`.

Accessor `ConfigService.PrivateStoragePolicy()` возвращает отдельную копию; settings дополнительно копирует policy любого provider при конструировании и не сохраняет внешнюю изменяемую ссылку. Поля policy — строки, поэтому поверхностная копия полностью отделяет snapshot. Ненулевой указатель на zero-value policy отклоняется даже при пустой конфигурации; отсутствие provider (`nil`) допускается только для pathless stores. `NewPrivateStoragePolicy` требует все три назначения; обычный `NewConfigService` без полного layout всё равно резервирует собственный config directory. Native-alias containment сравнивает идентичности существующих предков вместе с оставшимися компонентами, в том числе когда оба конечных пути ещё отсутствуют. Windows-only regression проверяет `subst` и отказ Add/create до `Mkdir`; на других ОС она не исполняется.

## HTTP import Web Clipper

`RegisterClipImportRoutes` регистрирует ровно `/api/clipper/v1/imports` (GET/POST/OPTIONS), с loopback Host, extension Origin/Bearer guard и setup guard; endpoint по умолчанию `http://127.0.0.1:8080`. Авторизация и setup предшествуют чтению тела/созданию staging, после staging тот же секрет повторно проверяется перед Import. Внешняя обёртка ставит `Cache-Control: no-store` также на ошибки guards и OPTIONS; JSON-ответы имеют `Content-Type: application/json`.

POST принимает multipart с одной JSON-частью `manifest`: `Content-Disposition: form-data; name="manifest"`, `Content-Type: application/json` (опционально `charset=utf-8`). Объявленные бинарные части принимаются в любом порядке относительно manifest, с точным ASCII ID в имени части и согласованным MIME без параметров; отсутствующие/неописанные/повторные части отклоняются. Filename необязателен и игнорируется. HTTP importer публикует проверенные изображения; manifest-only вызов без payload их не принимает. Стандартный multipart writer формирует CRLF; closing без padding SP/HTAB, с CRLF или сразу EOF. LF-only closing, обрыв и любой epilogue запрещены; бинарная строка, только похожая на closing, допустима. Wire cap 64 MiB включает JSON escaping, MIME headers и framing; лимит Markdown 5 MiB относится к декодированным UTF-8 байтам. Поэтому JSON-часть может превышать 5 MiB. Строгий публичный `service.DecodeClipperManifest` проверяет полную схему, Unicode, дубли/неизвестные/null поля и trailing JSON. Canonical digest v1 в [API](api.md) остаётся точным определением immutable payload.

### Staged assets: валидация и публикация

Границы владения: HTTP handler владеет только request directory и временем жизни заимствованного `StagedClip`; `ClipImportService` — durable owned sources, схемой private plan/checkpoint и файловой публикацией; `ClipImportRepository` — уникальным claim, context-aware SQLite IO и CAS, но не интерпретацией journal и не filesystem cleanup. `SettingsService` предоставляет проверенный scoped destination под lifecycle locks; `NoteService` обновляет индекс только активной базы. Наличие файла или совпадение hash само по себе не передаёт владение importer.

`service.StageClip(ctx, root *os.Root, reader *multipart.Reader) (*service.StagedClip, error)` требует приватный pinned request root и raw multipart reader после проверки outer envelope/wire cap. Он не публикует файлы в базе и не использует journal. Потоковый `io.LimitReader(limit+1)` ограничивает каждый asset 10 MiB и суммарно 50 MiB, одновременно пишет SHA-256 и исходные bytes в эксклюзивный файл `asset-<random>` (0600). Карта `StagedClip.Assets` связывает точные ID с `StagedClipAsset{Descriptor, Name, Extension}`. `OpenAsset(id)` открывает bytes read-only через pinned root; возвращённый файл закрывает consumer. Никаких fetch по URL и перекодирования.

После сверки descriptors валидатор заново открывает приватный файл и на одном descriptor выполняет structural preflight, reset позиции и полное декодирование. `clip_import_image_format.go` проверяет PNG CRC/chunk order/IEND и точный zlib-поток IDAT, JPEG framing/EOI, GIF canvas/каждый frame/data blocks/trailer, WebP RIFF lengths/padding и внутренние размеры. Обычные PNG/JPEG/GIF декодируются стандартной библиотекой, WebP — pure-Go `golang.org/x/image/webp` v0.47.0 (Go 1.26.0; его module требует x/sys v0.49.0). Полный `gif.DecodeAll` проверяет также поздние кадры. Static PNG/JPEG/WebP и animated GIF поддерживаются; APNG chunks и WebP animation flags/ANIM/ANMF дают 415. Несоответствие signature/MIME/hash/size, corruption/truncation и trailing container bytes дают безопасный 400.

До выделения пикселей применяются включительные бюджеты: 40000000 пикселей на изображение, GIF canvas и каждый frame; максимум 100 GIF frames и 200000000 суммарных frame pixels. Превышение — 413. `info.limits` сообщает `image_pixels`, `gif_frames`, `gif_total_pixels`. Полное декодирование сериализовано одним process-wide context-cancelable gate; IO проверяет context, lifecycle/SQLite locks не удерживаются. Preflight GIF сканирует все кадры до `DecodeAll`, не выделяя пиксельных буферов; отдельный constant-memory LZW scan требует корректный end code, точное число пикселей и отсутствие лишних data blocks, которые стандартный decoder может терпеть.

Handler вызывает optional `handlers.StagedClipImporter.ImportStaged(context.Context, *service.StagedClip)` при наличии интерфейса. Payload/root заимствованы только на время вызова; consumer обязан закрыть открытые assets до возврата. Handler удаляет request directory и закрывает handles после callback на любом исходе; при ошибке `StageClip` дополнительно удаляет только собственные generated assets. Реальный `ClipImportService` реализует этот интерфейс; прямой `Import(ctx, manifest)` по-прежнему отклоняет attachments без проверенного payload. Receipt lookup выполняется до новой staging ownership и проверки устаревшего назначения. Для новой попытки verified bytes копируются вне publication locks в `owned-<random>/source-<random>` внутри `SettingsService.OpenPrivateStagingRoot()`. SHA/size проверяются повторно; файлы и оба каталога синхронизируются до claim. Cleanup проигравшего identical-ID contender удаляет только его случайный каталог. Неизвестный результат Claim удерживает источники, пока lookup не докажет отсутствие claim либо другого владельца.

#### Приватный journal v2 и local recovery

Новые text-only immutable plans v1 имеют `{version:1,note,markdown,warnings}`; checkpoints без version остаются совместимыми с прежним форматом. Новые image plans имеют `{version:2,note,markdown,warnings,staging_id,staging_identity,assets:[{file,source:{name,identity},ids}]}`. `note` и каждый `file` содержат `{parent,filename,temp_name,sha256,size}`; hashes — bare hex, native identity — portable `{device,file}`. Все final paths, случайные O_EXCL temp names, source names/identities, SHA/size, группы дедуплицированных IDs и ordered warnings фиксируются immutable Claim до первого создания assets directories или файлов в базе. Markdown (до 5 MiB) хранится inline. Assets сортируются по final filename, IDs — лексикографически. Source names и staging ID генерирует сервер; recovery reader проверяет version, безопасные одиночные компоненты, native ownership, hash/size, а не разрешает произвольные клиентские/исторические абсолютные пути. Orphan без достоверного claim не является разрешением на автоматическое удаление.

`warnings` — additive presence-aware поле обоих планов, без миграции SQLite или изменения публичного протокола. Новые plans всегда сохраняют массив, включая явный `[]`; это точные `external_images[].reason` с исходным порядком и дубликатами, без сортировки/дедупликации. Success receipt читает warnings из persisted plan, а не из live manifest. Вместе с immutable Markdown, note/source metadata и историческими `ImportID`/`BaseName`/`NotePath` из record этого достаточно для восстановления полного result после потери HTTP request directory. Старые v1 и ранние v2 plans могут не иметь поля; отсутствие или `null` означает **неизвестные**, а не пустые warnings. Local recovery оставляет незавершённый legacy import в manual recovery: digest сам по себе warnings не восстанавливает, подставлять `[]` нельзя. Уже завершённый receipt остаётся авторитетным и воспроизводится точно; старые terminal plans/receipts не переписываются.

Checkpoint v2: `{version:2,step,note,assets:[progress],indexed}`; порядок assets совпадает с immutable plan. Progress содержит `{identity,sha256,size,published,publication_uncertain,durable}`. Шаги: `creating`, `writing`, `written`, `published`, `receipt`, `cleaned`, `recovery`. После каждого create/write/publish сохраняется полный снимок; crash может опередить checkpoint, поэтому recovery сопоставляет plan, actual entries и native identity. Нельзя трактовать `published:false` как доказательство отсутствия rename, если `publication_uncertain:true`.

Сетевое чтение, structural preflight и **полное декодирование** изображений завершаются до захвата publication coordinator. Копирование verified bytes в durable private owned sources также идёт до publication locks; краткий `OpenPrivateStagingRoot` отдельно проверяет policy под lifecycle locks и освобождает их до IO. Затем порядок публикации: `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`, без обратного входа в публичные сервисы. Repository checkpoints/transitions сравнивают `(state, updated_at)` в guarded UPDATE; успешная мутация инвалидирует прежнюю version, после неё запись перечитывается. Context cancellation и неопределённый ответ SQLite не дают права объявлять незафиксированный terminal outcome или удалять возможные evidence/sources.

Publication удерживает прежний порядок `coordinator -> settings.mu -> notes.baseMu` внутри scoped destination callback, без upward service re-entry. Existing assets/images проверяются по pinned components без symlink/.git aliases; отсутствующие каталоги создаются только после claim. Binary copy идёт из pinned owned source, проверяет фактически записанные SHA/size, затем fsync/close, no-replace rename, parent fsync. Все assets публикуются до note; только активная база переиндексируется до receipt. При определённом отказе до note publication допустим rollback своих неизменённых images в той же критической секции по identity/hash. Любой uncertain rename, чужое изменение или уже опубликованная заметка удерживают evidence/sources; поздний rollback запрещён. Источники удаляются только после durable success receipt либо подтверждённого safe rejected в исходной критической секции; административный abandon ничего не очищает. Index/receipt failures сохраняют источники после handler cleanup. Recovery reader/roll-forward, автоматический startup и recovery/abandon UI реализованы.

`(*ClipImportService).RecoverLocal(ctx context.Context) error` обрабатывает `prepared`, `publishing`, `recovery_required` из SQLite. Он берёт `coordinator -> settings.mu`, затем scoped current configured snapshot берёт `notes.baseMu`; repository/SQLite вызываются только ниже этих locks. Не требует process-local destination revision registry и не вызывает HTTP/public staging opener повторно под locks. Private staging открывается без создания, после проверки той же private-storage policy, pinned directory/source native identity и SHA/size. Нет fetch/внешнего image IO. `BeginReplaceAllContext` делает ожидание соединения/подготовку active index отменяемым; scan failure сохраняет safe manual state, DB failure/cancel возвращается вызывающему коду без выдуманного terminal outcome.

| Наблюдение recovery | Решение |
|---|---|
| Terminal succeeded/rejected | Пропустить без проверки plan/files/index/private cleanup; receipt исторически авторитетен |
| Текущая настроенная база имеет сохранённый native root ID | Scoped recovery; переименование/перемещение с rebind допустимо, result сохраняет исходные base/path |
| Забытый/недоступный/заменённый корень, Git path/identity conflict gate | Durable `clipper_recovery_required`, без worktree effects и без fallback на historical pathname |
| Все final entries имеют записанные native IDs и planned SHA/size; temp отсутствуют | Повторить fsync, active reindex, CAS checkpoints и success receipt с `sha256:<hex>` и persisted ordered warnings |
| Own temp имеет known ID и полный planned hash (checkpoint отстал) | Синхронизировать и no-replace publish; assets всегда перед note |
| Own partial temp совпадает с checkpoint ID/hash/size | Дозаписать из immutable Markdown/private source с checkpoint перед эффектами; никогда не перезаписывать чужой inode |
| Нет entry и нет прежнего native ID | Создать только planned O_EXCL temp из полного журнала/проверенных private sources |
| Известный inode исчез, hash/identity изменились, одновременно temp/final, source потерян/изменён | Manual recovery, сохранить byte evidence; public rollback/удаление запрещены |
| O_EXCL create завершён до первого identity checkpoint | Владение недоказуемо даже при совпадающем hash; manual recovery вместо присвоения inode |
| Неизвестная version/schema, duplicate/unknown/missing/null fields, несовпадение row/plan, неизвестные warnings | Manual recovery без изменения immutable plan/checkpoint и private evidence |
| Rename вернул любую ошибку (включая EEXIST или потерю acknowledgement) | Сохранить uncertain checkpoint и safe manual state; следующий вызов сверяет реальные locations/identity |
| Ошибка SQLite или context cancellation | Вернуть error; сохранить durable progress для следующего вызова |

Reader строго проверяет envelope, v1/v2 plan/checkpoint schemas, portable paths, IDs, bare SHA-256, размеры/лимиты, порядок assets/IDs и checkpoint boundaries. Request digest проверяется как bare SHA-256; пересчитать исходный canonical manifest из этого журнала невозможно (source URL/captured_at в нём отдельно не сохранены), поэтому reader не заявляет такой проверки: Markdown проверяется по note hash/size и row, assets — по полному плану/источникам. Immutable plan/references не переписываются. Data errors одной записи не мешают recovery других корней; невозможность durably записать safe status является fatal DB error.

После durable receipt допускается только очистка перечисленных собственных private sources по identity/hash и `rmdir` того же проверенного staging directory. Чужие entries/подменённые inode/aliases не удаляются; рекурсивной очистки нет. Crash после receipt до cleanup может оставить private orphan: повторный вызов пропускает terminal запись, не меняет историческое evidence и не создаёт удалённые впоследствии public files. Crash между truncate/write и новым checkpoint с недоказуемыми partial bytes также требует manual recovery: это не атомарная транзакция файлов и SQLite.

Startup в `cmd/api/main.go` создаёт единственные destination service, import repository и importer до запуска workers. Порядок: `GitManager.RecoverLocal(ctx, snapshots)` → `ClipImportService.RecoverLocal(ctx)` → `GitManager.Start()` → первичный фоновый `NoteService.SyncFS()` → HTTP. Оба recovery синхронны и локальны; существующая глобальная private-storage policy резервирует config/SQLite/staging вне всех баз. Fatal DB/context error возвращается через безопасный `gitLifecycleError` до HTTP без приватной диагностики. Durable per-record `recovery_required` не прерывает запуск: Bearer GET status и same-origin list доступны после recovery, существующий service gate блокирует новые публикации в unresolved root, несвязанные базы продолжают обычную работу. Handler/auth wiring использует тот же importer и destination registry. Git conflict gate восстанавливается раньше importer и запрещает worktree effects; поздний roll-forward не удаляет уже закоммиченные/отправленные public files. Успешно восстановленный клип и assets участвуют в обычном Git sync без переключения active runtime/index для неактивной базы.

Same-origin `GET /api/clipper/imports` и `POST /api/clipper/imports/abandon?id=...` зарегистрированы через `RegisterClipImportManagementRoutes` с тем же singleton importer. `RequireLocalOrigin` и запрет Authorization предшествуют method/setup checks, затем работает `RequireSetup`; Clipper credential, даже действующий и без Origin, не даёт административного доступа. Список читает `store.ListUnfinished(ctx)` без coordinator и преобразует private records в минимальные summaries: import_id/base/relative path/state/error. Prepared/publishing отображаются как pending; recovery error фиксирован и не копирует private diagnostics. Query/body/UUID и точные HTTP responses описаны в API docs.

`ClipImportService.Abandon(ctx,id)` берёт только `coordinator -> repository/SQLite`, проверяет отмену контекста, перечитывает запись и выполняет CAS `recovery_required -> rejected` с `clipper_import_abandoned` и предупреждением `files may already exist`. Settings/base locks, filesystem и индекс ему не нужны. Повтор уже abandoned идемпотентен; unknown ID — 400 на HTTP boundary, другие состояния/устаревший CAS — 409 `clipper_import_not_abandonable`. Public/private файлы, plan/checkpoint и evidence сохраняются. Terminal row авторитетен для startup recovery и same-ID replay после restart; новая публикация старого ID невозможна. Root gate автоматически освобождается только для этой записи через ListUnfinished; другой unfinished hold и Git conflict/paused не снимаются.

`ClipperSettingsSection.svelte` включает дочерний `ClipperRecoverySection.svelte` с загрузкой, ручным reload/retry и явным Modal confirm/cancel. Только recovery_required предлагает abandon. Поздние async responses после destroy игнорируются, автоматических POST/retry нет; после подтверждённого abandon перечитывается список, а сообщение сохраняет предупреждение о возможных файлах. Service tests используют реальные SQLite/crash fixtures, проверяют byte/inode evidence, reopen/replay, независимые root holds и гонку с RecoverLocal; handler/cmd tests проверяют запрет Bearer и routing, Svelte tests — подтверждение/отмену/focus/unmount.

`ClipImportService` ищет durable receipt по ID/digest до текущей проверки назначения/revision и видимости parent. Новый импорт требует существующий видимый parent (корень `""` разрешён; dot/`assets` components запрещены), актуальную destination_revision, private-storage policy и Git conflict gate; не переключает активную базу и не создаёт папки. Порядок locks: `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`. Network/body IO идёт вне них. Markdown сохраняется побайтно: title/YAML должны быть уже включены клиентом, URL сервер не загружает.

POST возвращает `201` с `ClipperImportResult` при новой публикации, `200` с тем же телом при успешном same-ID/same-payload replay, `202` с **APIError**, не result, при pending: `{"code":"clipper_import_pending","message":"Clipper import pending"}`. GET с единственным `id=<lowercase-UUID-v4>` (raw query до 8192 байт) возвращает `200` и `ClipperImportStatusResponse`: `pending` без result/error, `succeeded` только с result, `rejected`/`recovery_required` только с error. Неизвестный/невалидный ID даёт `400`, что не доказывает отсутствия ещё выполняющегося POST. Все публичные ошибки безопасны, без private paths/journal/token.

Timeout, network error и неразрешённый 5xx требуют GET/точного replay прежнего ID с неизменными payload и destination_revision. Не удалять expected checks и не обновлять revision/назначение автоматически. Только подтверждённый rejected позволяет исправить запрос с новым ID и заново подтвердить назначение; pending/recovery_required и один `500` не являются таким подтверждением. Другой digest под старым ID — `409 clipper_request_changed`. Исторический успешный receipt возвращается без перепубликации даже после restart/rename/удаления файла или смены действующего токена.

Revision нового result вычисляется общим `noteRevision` как `sha256:<hex>` по точным байтам Markdown и совпадает с ревизией `NoteService.GetNote`. Private file/journal hashes и canonical request digest остаются bare hex. Replay возвращает сохранённый исторический result без преобразования.

Private SQLite journal хранит точный Markdown, план/identity/hash, состояния `prepared`/`publishing`, checkpoints и durable receipt. Временный HTTP wire body не является recovery-копией текста; после его удаления текст остаётся в private journal, а незавершённый image import сохраняет собственные источники. Ошибки после возможной публикации не приводят к позднему удалению файлов; unresolved import блокирует новую публикацию в тот же корень. Roll-forward, startup recovery, same-origin list/abandon endpoints и recovery UI реализованы; Chromium-расширение и preview resolver вложенных относительных image paths (5.7) остаются **PLANNED**.

### Backup, restore и совместимость схемы

Полная практическая инструкция и пути приведены в [руководстве пользователя](user.md#резервная-копия-и-восстановление). Backup делается вручную после штатного завершения процесса: HTTP drain, остановка/ожидание Git workers и закрытие SQLite должны закончиться **до** копирования. Один согласованный комплект включает resolved config directory (`--config`/XDG), `~/.igonotes/metadata.db` с необходимыми существующими sidecars (`-wal`, `-shm`, `-journal`, `-mj*`), private `clipper-staging` с pending owned sources и все настроенные `bases[].path` целиком, включая assets и Git. Базы не обязаны лежать в appDataDir. Live-копия одного SQLite-файла или частичный restore не обеспечивают согласованности файлов и journal; автоматического backup нет.

Restore требует исходных путей и, для доказуемого roll-forward, сохранённых native identities корня/public/temp/private sources вместе с SHA/byte count. Побайтная копия на том же pathname может иметь другую identity, особенно на другой FS: recovery намеренно fail-closed, без hash-only adoption и без автоматического переписывания ownership. Сначала сохраняют текущий комплект с новой работой, затем восстанавливают один полный backup; orphan sources после terminal crash не очищают через слепой `RemoveAll`, terminal row пропускается по правилам выше.

Миграция **v5** в `internal/repository/db.go` добавляет `clipper_connections` и `clip_imports`; private plan v1/v2 — отдельное версионирование, не schema version. `InitDB` требует непрерывную историю `1..k` в пределах миграций бинарника: старый бинарник с максимумом v4 отвергает v5. Downmigration нет; rollback версии — восстановление pre-upgrade согласованного комплекта со старым бинарником, с предварительным сохранением текущих файлов, а не удаление таблиц/версии или замена только БД.

### Локальный request fixture

`testdata/clipper/text-import.json` — полный manifest без секрета, с фиксированным валидным UUID v4, базой `target`, готовым UTF-8 Markdown/YAML и явно нерабочим placeholder destination_revision. Для ручной отправки:

1. Запустите IGoNotes, завершите setup, создайте подключение в `Настройки → Веб-клиппер`. Сохраните выданный секрет вне fixture и репозитория, например в переменной окружения `CLIPPER_SECRET`. Используйте точный loopback endpoint/порт своей установки.
2. С этим Bearer выполните `GET /api/clipper/v1/bases`, затем `GET /api/clipper/v1/folders?base=<URL-encoded-реальное-имя>`. Явно подтвердите базу/папку. В локальной копии fixture задайте реальное имя `base`, возвращённый `destination_revision` и существующий `parent` (или `""`); placeholder и `opaque` из примеров не являются рабочими handles.
3. Перед **новой** попыткой сгенерируйте свежий lowercase UUID v4, например стандартным Python `uuid.uuid4()`, и задайте `import_id`. Выберите свободный filename, если `text-import.md` уже существует. После начала попытки сохраните эту копию неизменной для status/replay.
4. Передайте локальную копию как единственную multipart-часть с JSON MIME; не используйте обычный form field без Content-Type. Пример из корня checkout, где `text-import.local.json` — подготовленная копия, а `CLIPPER_SECRET` содержит реальный секрет:

   ```bash
   curl --include --header "Authorization: Bearer ${CLIPPER_SECRET}" \
     --form 'manifest=@text-import.local.json;type=application/json' \
     http://127.0.0.1:8080/api/clipper/v1/imports
   ```

   Для статуса отправьте GET на тот же endpoint с `?id=<UUID-из-копии>` и тем же Bearer. CLI без Origin допустим; реальный extension Origin должен пройти guard. На uncertain outcome используйте прежнюю копию, а не новый UUID или свежую revision.

`internal/handlers/clip_import_fixture_test.go` читает fixture с диска через тот же публичный строгий decoder, заменяет только process-local revision реальным folders handle, использует базу `target` тестового окружения и реальный service/SQLite/router. Проверяет 201 → 200 с побайтно одинаковым receipt, отсутствие дублей, точный Markdown/hash, index path и точный GET snapshot. Секрет создаётся только в изолированном тесте. Проверки без frontend-команд и внешней сети:

```bash
go test ./internal/handlers -run 'ClipImport.*Fixture|ClipImportHTTP' -count=1
go test ./internal/docs -count=1
```

## Архитектура frontend

`web/src/App.svelte` управляет состояниями экрана без client-side router:

- `loading`: загрузка конфигурации;
- `error`: ошибка загрузки и повторный запрос;
- `setup`: обязательный трехшаговый мастер;
- `editor`: дерево заметок и редактор либо полноэкранная рабочая область Git-конфликтов;
- `settings`: полноэкранные настройки баз, Git-синхронизации и раздел «О программе».

Редактор не отображается, пока `setup_completed` не станет истинным. `web/src/lib/setup/SetupWizard.svelte` проводит пользователя через создание или подключение первой базы, ввод имени и каталога, проверку и завершение. `BaseForm.svelte` и `DirectoryField.svelte` переиспользуются в мастере и настройках, включая системный выбор каталога с ручным вводом как запасным вариантом.

`App.svelte` хранит содержимое активной заметки вместе с ее `revision`. Сохранение передает revision серверу; ответ `409 note_changed` переводит заметку в состояние stale recovery. Второй и единственный другой триггер -- терминальный Git-статус, в чьих `changed_paths` есть активная заметка; наблюдателя произвольных изменений файловой системы нет. `StaleNoteDialog.svelte` показывает локальный и дисковый тексты, позволяет загрузить диск, подтвержденно перезаписать его или сохранить ручное объединение. При удалении файла на диске окно допускает только закрытие заметки; переходы блокируются до завершения восстановления.

Опрос статусов Git передает терминальные `changed_paths` в `App.svelte`. Обработанные пути кешируются по паре пути репозитория и `operation_id`, поэтому обновление дерева и перезагрузка затронутой заметки выполняются один раз для каждой операции. Чистая активная заметка перезагружается, а грязная переводится в stale recovery. При переключении базы во время Git-конфликта черновик активной заметки сохраняется в памяти и восстанавливается при возврате.

`web/src/lib/settings/SettingsWorkspace.svelte` и `BaseCard.svelte` реализуют добавление, подключение, переименование, изменение пути, переключение и забывание баз без перезапуска. Переходы из редактора сначала завершают ожидающие загрузки и сохраняют текущую заметку; ошибка сохранения отменяет переход. Забывание меняет только конфигурацию и не удаляет файлы. `web/src/lib/git/GitConflictWorkspace.svelte` загружает конфликты, выбирает resolver по типу файла, переключает базы и принимает завершение либо отмену слияния; до терминального статуса операции основной редактор не возвращается.

`web/src/lib/DialogShell.svelte` -- общий доступный каркас модальных окон. Он назначает `aria-modal`, делает фон inert с учетом вложенных окон, устанавливает начальный фокус, удерживает фокус по `Tab`, восстанавливает его после закрытия и отключает отмену во время операции.

Все HTTP-вызовы frontend собраны в `web/src/lib/api.js`. Класс `ApiError` сохраняет `status`, `code`, `message` и `field`; именованные wrappers покрывают заметки, конфигурацию, setup, базы и системный выбор каталога.

Vitest 4, jsdom и Testing Library запускаются в Node.js 24 и проверяют компоненты и frontend-модули в файлах `web/src/**/*.test.js`, включая `App`, редактор, stale recovery, Git-конфликты, setup, настройки и API wrappers.

## REST API

| Метод | Путь | Описание |
|:---|:---|:---|
| GET | `/api/notes` | Получить дерево заметок |
| POST | `/api/notes` | Создать заметку или папку |
| GET | `/api/note?id=...` | Получить `content` и `revision` |
| DELETE | `/api/note?id=...` | Удалить заметку или папку |
| POST | `/api/save` | Сохранить с `expected_revision`, получить новую `revision` |
| PUT | `/api/rename` | Переименовать заметку или папку |
| POST | `/api/sync` | Синхронизировать файловую систему и метаданные |
| GET | `/api/info` | Получить путь текущей базы, название и версию приложения |
| GET | `/api/raw?path=...` | Получить файл из текущей базы |
| POST | `/api/assets` | Загрузить изображение |
| GET/PUT | `/api/config` | Получить/сохранить конфигурацию |
| POST | `/api/setup` | Завершить первичную настройку с первой базой |
| POST/PUT/DELETE | `/api/bases` | Добавить, изменить или забыть базу |
| POST | `/api/bases/switch` | Переключить активную базу |
| POST | `/api/system/select-directory` | Открыть системный выбор каталога |
| POST | `/api/git/probe` | Проверить репозиторий без изменений |
| PUT/DELETE | `/api/git/config?base=...` | Сохранить настройки и поставить initialize в очередь / отключить Git |
| GET | `/api/git/status?base=...` | Получить статус одной базы; без `base` — всех баз |
| POST | `/api/git/sync?base=...` | Поставить синхронизацию Git-базы в очередь |
| POST | `/api/git/resume?base=...` | Явно снять паузу и поставить sync в очередь |
| GET | `/api/git/conflicts?base=...` | Получить конфликты текущей Git-операции |
| PUT | `/api/git/conflicts/resolve` | Применить решение конфликта |
| POST | `/api/git/conflicts/complete?base=...` | Поставить завершение слияния в очередь |
| POST | `/api/git/conflicts/abort?base=...` | Поставить отмену слияния в очередь и приостановить sync |

Note API, кроме `/api/info`, закрыт setup guard до завершения мастера. API принимает только запросы с допустимым локальным origin.

Полный контракт, включая `/api/git/probe`, PUT/DELETE `/api/git/config` и `/api/git/resume`, приведен в [спецификации API](api.md). GET заметки возвращает `content` и `revision`; POST save передает `expected_revision` и получает новую `revision`. Несовпадение возвращает `409 note_changed` без перезаписи. Клиент перечитывает диск и сохраняет восстановленный текст с новой ревизией после явного подтверждения пользователя.

## Архитектура Git-интеграции

### Запуск системного Git

`internal/git` — единственная граница запуска системного Git в приложении.

`internal/git/CommandRunner` запускает executable `git` с отдельным массивом `argv` через `exec.CommandContext`, никогда через shell. Рабочий каталог канонизируется; локальные и сетевые команды имеют отдельные таймауты (15 и 60 секунд), ограниченный вывод и безопасные ошибки с редактированием секретов. `Command.Stdin == nil` дает EOF, а не наследование терминала. Окружение всегда содержит `GIT_TERMINAL_PROMPT=0`; авторизация должна заранее работать через SSH-agent, credential helper или Git config пользователя.

В Linux и macOS команда получает собственную process group; отмена завершает группу, включая дочерние процессы. В Windows процесс создается с `CREATE_SUSPENDED`, присоединяется к Job Object с `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` **до** вызова `ResumeThread` для единственного начального потока. Ошибка присоединения или resume завершает и собирает приостановленный процесс; отмена ожидает завершения launch gate. После успешного старта runner выполняет один `Wait`, синхронизирует callback отмены и закрывает объект дерева процессов.

Git hooks создания коммитов (commit hooks) и настроенная подпись коммитов остаются активны. Push использует `--no-verify`, поэтому локальный `pre-push` пропускается. Сбой hook или подписи может завершить операцию ошибкой; приложение не обещает отключения всех hooks.

### Владельцы состояния

- `internal/git/Service` выполняет одну операцию одного репозитория: initialize, sync, conflict complete/abort либо recovery. Он получает неизменяемый снимок конфигурации, callback долговечного checkpoint и транзакцию рабочего дерева; не владеет общей очередью или расписанием.
- `internal/service/GitManager` владеет единственным последовательным FIFO worker, дедупликацией по канонической идентичности репозитория, долговечной очередью и терминальными статусами в SQLite. Его autosync scheduler обслуживает все настроенные базы, включая неактивные, и сверяет расписание с опубликованными изменениями конфигурации. Перед выполнением worker повторно проверяет снимок и fingerprint.
- `internal/service/SettingsService` — владелец Git-конфигурации: валидирует и сохраняет URL, ветку, интервал и шаблон, предоставляет Git snapshots и публикует изменения для manager/scheduler. Общий PUT `/api/config` не меняет Git-поля; Git-настройки проходят через `/api/git/config`. Сохранение настроек предшествует постановке initialize в очередь, поэтому отказ очереди не означает откат конфигурации.
- `NoteService` владеет доступом к активной базе и атомарной транзакцией изменения файловой системы с переиндексацией; `BaseOperationCoordinator` согласует операции Git и жизненный цикл баз. Конфликтный snapshot запрещает обычные записи в конфликтное дерево без повторного захвата coordinator.

Порядок блокировок фиксирован: `coordinator -> SettingsService.mu -> NoteService.baseMu -> repository/SQLite`. Callback нижнего уровня не должен захватывать coordinator или вызывать верхний слой. Сетевой fetch выполняется **до** write lock заметок; snapshot/commit, переключение и merge выполняются в транзакции рабочего дерева, затем reindex и освобождение note write lock; сетевой push выполняется **после** reindex/unlock. Это не удерживает блокировку чтения и сохранения заметок на время сети. Операция базы при этом остается сериализованной coordinator.

Git сохраняет все неигнорируемые файлы базы (`git add -A`), включая assets. При initialize (подключении или перенастройке) резервная ссылка `refs/igonotes/backups/...` создается условно, только при наличии локального снимка, требующего защиты перед переключением или слиянием; это Git ref, не копия каталога. Обычный sync фиксирует локальные изменения и выполняет слияние, но не создает новые backup refs. При merge conflict обычные записи блокируются до явного resolve/complete/abort. Перезапуск восстанавливает состояние локальными проверками, не удаляет Git lock/state файлы и не возобновляет паузу автоматически. Shutdown отменяет текущую операцию, записывает `operation_interrupted` и не запускает заключительный commit/push.

## Хранение данных

- Markdown-файлы и `assets/images/` находятся внутри каталогов баз, указанных в конфигурации.
- SQLite с метаданными находится в `~/.igonotes/metadata.db`.
- Конфигурация находится в `<os.UserConfigDir()>/igonotes/config.json`, если каталог не переопределен флагом `--config`.

SQLite (дерево, Git-очередь, checkpoints и статусы) и конфигурация приложения находятся вне каталогов баз и не входят в Git-коммиты базы.

## Тестирование

Frontend-тесты и режим наблюдения:

```bash
cd web
npm test
npm run test:watch
```

Проверки перед merge из корня проекта (Go 1.26+, Node.js 24):

```bash
npm --prefix web run verify
make test
make test-git
make test-race
make vet
make verify
make all
```

`npm --prefix web run verify` запускает frontend-тесты и сборку. `make test`, `make test-git`, `make test-race` и `make vet` сначала собирают embedded frontend; `make verify` объединяет frontend-тесты/сборку, полный Go suite, race detector и vet. `make all` собирает бинарник.

Для обычного `go test ./...` Git-интеграционные тесты опциональны: при недоступном executable либо Git старее **2.28** они пропускаются. `make test-git` включает `IGONOTES_REQUIRE_GIT_INTEGRATION=1`: native Git обязателен, отсутствие или неподдерживаемая версия приводят к ошибке. Этот режим применим и к полному suite:

```bash
IGONOTES_REQUIRE_GIT_INTEGRATION=1 go test ./... -count=1
```

Интеграционные тесты используют реальный системный Git, изолированное окружение, временные локальные bare remotes и рабочие каталоги. Внешняя сеть и пользовательские credentials не нужны; `go-git` не используется. Тесты `internal/docs` определяют корень checkout один раз при инициализации пакета: сначала по абсолютному пути исходника из `runtime.Caller`, а при недоступном исходнике или модульном пути с `-trimpath` — поиском вверх от стартового рабочего каталога. Корень проверяется по `module IGoNotes` в `go.mod` и файлам документации и остается неизменным при последующей смене CWD в тестах. Перенесенный test executable с `-trimpath` нужно запускать внутри checkout; произвольный установленный бинарник без доступных исходников не может автоматически обнаружить их из любого каталога. Git и обязательный integration env для docs-тестов не нужны. Они проверяют текущие контракты и точные зеркала API/user/developer после нормализации CRLF. Jekyll-страница содержит только frontmatter `layout: default` и точные байты исходника, включая raw wrappers вокруг шаблонных переменных.

## Добавление функции

1. Добавьте или измените структуры и сервисы в `internal/`, затем зарегистрируйте handler и маршрут.
2. Добавьте именованный API wrapper в `web/src/lib/api.js` вместо прямых разрозненных вызовов `fetch`.
3. Реализуйте интерфейс в небольшом Svelte-компоненте под `web/src/lib/` и подключите его к экранному состоянию `App.svelte`.
4. Добавьте Go-тесты и Vitest component/module tests рядом с измененным кодом.
5. Запустите команды полной проверки.

Git API использует системный Git 2.28+, поэтому для настройки и синхронизации он должен быть доступен в `PATH` процесса IGoNotes. Для обычной локальной работы Git необязателен.

## Работа с Git

Проект использует ветки `develop` для разработки, `master` для стабильной версии и `pages` для Jekyll-документации.

CI выполняет native Git integration на трех ОС: Linux, macOS и Windows, с обязательным `IGONOTES_REQUIRE_GIT_INTEGRATION=1`, полным Go suite и повторными process-tree/two-device тестами. Отдельная Linux job запускает race/vet/build и шесть cross-build комбинаций Linux/Windows/macOS × amd64/arm64 (`CGO_ENABLED=0`). Кросс-компиляция проверяет сборку, а native jobs — выполнение платформенного кода.

Release запускается тегом `v*`. Job `build` имеет только `contents: read`, выполняет frontend/Go/race/vet проверки и собирает шесть архивов с checksums. Только job `publish` имеет `contents: write`: получает уже проверенные artifacts, проверяет точные шесть архивов и checksums и публикует release. В `publish` нет checkout или сборки исходного кода.
