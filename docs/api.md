{% raw %}

# Спецификация API IGoNotes

Документ описывает REST-интерфейс поставляемого приложения по обработчикам `internal/handlers`, моделям `internal/model` и Git-сервисам `internal/service` и `internal/git`.

## Базовый URL

```
http://127.0.0.1:8080
```

Все пути ниже абсолютны относительно этого URL и начинаются с `/api`. JSON-запросы передаются с `Content-Type: application/json`; загрузка файла использует `multipart/form-data`.

### Локальный доступ и первичная настройка

Все API-маршруты защищены `RequireLocalOrigin`: `Host` должен быть локальным (`localhost` или loopback IP). Если передан `Origin`, его схема, нормализованный хост и порт должны совпадать с запросом; `localhost` и `127.0.0.1` не взаимозаменяемы. `Sec-Fetch-Site: cross-site` отклоняется. Клиент без `Origin` допускается при локальном `Host`. Отказ — `403 forbidden_origin`; это не API для удалённого доступа или произвольного CORS.

Заметки и **все Git-маршруты**, включая probe и status, требуют завершённой первичной настройки (`428 setup_required`). `/api/info`, `/api/config`, `/api/setup`, `/api/bases`, `/api/bases/switch` и выбор каталога не обёрнуты в `RequireSetup`, но проверяют свои условия в сервисах. Неподдерживаемый метод возвращает `405 method_not_allowed` и заголовок `Allow`.

`id`, `parent_id`, `path` заметки, пути файлов и назначения разрешения конфликтов относительны **корню выбранной базы**, а не проекту или каталогу конфигурации. Они не могут выходить из базы, быть абсолютными или обращаться к компонентам `.git`; символьная ссылка не позволяет обойти границу базы. Путь самой базы в настройках, `base_path` и `repository_path` — пути каталогов файловой системы. Значения query-параметров необходимо URL-кодировать.

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
| GET | `/api/info` | 200 | Путь активной базы |
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

`POST /api/sync` обновляет только индекс файловой системы; Git-синхронизация запускается через `POST /api/git/sync`.

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

`POST /api/sync` выполняется без JSON-тела и возвращает `{"status":"ok"}` после пересканирования. `GET /api/info` возвращает, например, `{"base_path":"/home/user/notes/work"}` (до настройки путь может быть пустым).

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
