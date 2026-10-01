# Спецификация API IGoNotes

Данный документ описывает REST-интерфейс, предоставляемый сервером IGoNotes.

## Базовый URL

```
http://localhost:8080/api
```

## Получение дерева заметок

- **Метод**: `GET`
- **Путь**: `/notes`
- **Описание**: Возвращает древовидную структуру всех заметок и папок.
- **Ответ**:
```json
[
  {
    "name": "notes",
    "type": "dir",
    "children": [
      {
        "name": "idea.md",
        "type": "file",
        "path": "notes/idea.md"
      }
    ]
  }
]
```

## Получение заметки по ID

- **Метод**: `GET`
- **Путь**: `/notes/:id`
- **Описание**: Получает заметку по ID или пути.
- **Ответ**: Текстовое содержимое файла (Markdown).

## Создание заметки или папки

- **Метод**: `POST`
- **Путь**: `/notes`
- **Тело запроса**:
```json
{
  "path": "notes/new.md",
  "type": "file",
  "content": "# Новая заметка\n\nЭто содержимое..."
}
```
- **Заголовки**: `Content-Type: application/json`
- **Ответ**: `201 Created` при успехе.

## Обновление заметки

- **Метод**: `PUT`
- **Путь**: `/notes/:id`
- **Тело запроса**:
```json
{
  "content": "Обновлённое содержимое..."
}
```
- **Заголовки**: `Content-Type: application/json`
- **Ответ**: `200 OK` при успехе.

## Удаление заметки или папки

- **Метод**: `DELETE`
- **Путь**: `/notes/:id`
- **Ответ**: `200 OK` при успехе.

## Загрузка изображения

- **Метод**: `POST`
- **Путь**: `/assets`
- **Тело запроса**: `multipart/form-data` с файлом
- **Ответ**: `201 Created` с путём к файлу.

## Сохранение настроек

- **Метод**: `PUT`
- **Путь**: `/config`
- **Тело запроса**: JSON с настройками
- **Ответ**: `200 OK` при успехе.

## Синхронизация с Git

- **Метод**: `POST`
- **Путь**: `/git/sync?base=<name>`
- **Описание**: Ставит синхронизацию указанной базы с удалённым репозиторием в общую последовательную очередь.
- **Ответ**: `202 Accepted` с `operation_id`, `status` и `deduplicated`; результат выполнения доступен через `GET /git/status?base=<name>`.

## Git: автоматическая синхронизация и возобновление

| Метод | Полный путь | Назначение |
|:---|:---|:---|
| GET | `/api/git/status?base=<name>` | Текущий сохранённый статус базы; без `base` — статусы всех баз в порядке конфигурации |
| POST | `/api/git/sync?base=<name>` | Обычная ручная синхронизация; не обходит паузу |
| POST | `/api/git/resume?base=<name>` | Явное возобновление приостановленной синхронизации |

Маршруты требуют завершённой настройки и разрешённого локального origin. Для POST параметр `base` обязателен и указывается ровно один раз; имя следует URL-кодировать. Неизвестные и повторные query-параметры отклоняются.

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

## Обработка ошибок

- `400 Bad Request` — неверные параметры.
- `404 Not Found` — файл или ресурс не найден.
- `500 Internal Server Error` — ошибка на стороне сервера.

---

> **Примечание**: Все пути относительны корня проекта. Сервер автоматически управляет структурой каталогов.

## Git: конфликты слияния

Все следующие маршруты находятся под `/api`, требуют завершённой настройки базы и локального origin. Они доступны только когда статус базы равен `conflict`.

Стадии индекса Git фиксированы: `base` — стадия **1**, `local` — стадия **2**, `remote` — стадия **3**. Сервер не выбирает вариант автоматически.

### `GET /git/conflicts?base=<name>`

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
      "local": {"path": "notes/draft.md", "oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mode": "100644", "size": 6, "content": "edit\n", "preview_truncated": false},
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
      "local": {"path": "notes/renamed.md", "oid": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "mode": "100644", "size": 6, "content": "edit\n", "preview_truncated": false},
      "actions": ["local", "manual", "delete"]
    }
  ],
  "can_complete": false
}
```

`kind` принимает `content`, `add_add`, `modify_delete` или `rename_delete`; `content_kind` — `text` или `binary`. Для валидного UTF-8 текста больше 1 MiB `content` опускается и `preview_truncated` равно `true`.

### `PUT /git/conflicts/resolve`

Разрешает ровно один логический конфликт. Обязательны `base`, `operation_id`, `conflict_id`, `path` и `action`. Идентификаторы и OID должны соответствовать текущему снимку; устаревшие значения не выбирают изменённый файл.

Выбор local (стадия 2) или remote (стадия 3) требует соответствующего OID и `result_path`:

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

`keep_both` доступен для конфликтов с бинарным содержимым и для всех конфликтов `add_add`; он требует разных выходных путей и обоих OID:

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

`delete` не имеет полей действия и удаляет логический путь. Отклоняются абсолютные пути, traversal, компоненты `.git`, пересечения с другим неразрешённым конфликтом и не принадлежащие конфликту существующие назначения.

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

### `POST /git/conflicts/complete?base=<name>`

Возможен только при `can_complete: true`. Маршрут ставит в очередь долговечную операцию и отвечает `202 Accepted`:

```json
{
  "operation_id": "fedcba9876543210fedcba9876543210",
  "status": "queued",
  "deduplicated": false
}
```

Повторный запрос той же операции возвращает тот же ID с `deduplicated: true`. Выполнение создаёт двухродительский merge commit, перестраивает индекс заметок, пушит именно зафиксированный merge OID, обновляет trusted remote OID и публикует `ready`. `changed_paths` готового статуса — точный diff с исходным local commit.

### `POST /git/conflicts/abort?base=<name>`

Ставит в очередь локальную операцию отмены и возвращает ту же схему `202`. Она выполняет `git merge --abort`, перестраивает восстановленные пути, не выполняет push и публикует `paused`:

```json
{
  "base": "work",
  "repository_path": "/notes/work",
  "state": "paused",
  "operation_id": "fedcba9876543210fedcba9876543210",
  "stage": "completed",
  "changed_paths": ["notes/idea.md", "notes/todo.md"],
  "remote_oid": "2222222222222222222222222222222222222222"
}
```

После abort `HEAD` и рабочее дерево возвращаются к local commit, unmerged index entries исчезают, а remote и trusted remote OID не меняются. Обычные изменения заметок разрешены, однако `POST /git/sync?base=<name>` отвечает `409 git_paused` до явного `POST /git/resume?base=<name>`; abort не ставит sync в очередь.

### Ошибки и восстановление

Ошибки имеют форму `{ "code", "message", "field?" }`. Пути, OID, URL, команды и диагностика Git не раскрываются.

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
