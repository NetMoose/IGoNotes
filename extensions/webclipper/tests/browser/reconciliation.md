# Финальная сверка Web Clipper

Change: `add-chromium-web-clipper`, ветка `feat/chromium-web-clipper`.
Дата сверки: 2026-11-20. Это сверка текущей рабочей копии, не заявление о релизе.

## Требования и свидетельства

Все **56 сценариев** четырёх delta specs перечислены по точным названиям в
[acceptance.md](acceptance.md): web-clipping — 17, clipper-connection — 13,
clip-import — 21, private-storage-boundaries — 5. Маппинг указывает реальные
файлы тестов; исторические автоматические и новые ручные результаты различаются.
[Ручной отчёт](manual-results-2026-11-20.md) содержит подтверждения пользователя
для трёх браузеров и три найденных дефекта вёрстки.

| Контракт | Фактическая реализация / проверка |
| --- | --- |
| MV3, явный Article/Selection, безопасный Markdown | capture/convert, package/capture tests, browser normal |
| Явный выбор базы/папки, сохранность открытой заметки | destination service, import service tests, ручные active/inactive imports |
| Prepare до отправки, отдельный external consent | image-acquisition/preparation/UI tests, native CDN Deny/Allow |
| Durable drafts, ограничения 50 drafts / 250 MiB Blob | drafts tests, browser restart, ручной Vivaldi restart |
| Immutable attempt до POST, exact receipt/replay | delivery/recovery concurrency tests, browser delivery/restart |
| API v1, Bearer, loopback, Origin и запрет обычного API | guard/handler tests, hostile browser preflight |
| 5 MiB Markdown, 10 MiB asset, 50 MiB aggregate, 100 assets, 64 MiB multipart | Go/JS limits/manifest/assets tests, API docs |
| PNG/JPEG/WebP static, GIF; 40 МП / 100 GIF frames / 200 МП | Go structural/full-decode tests и JS image bounds tests |
| APNG/animated WebP запрещены, redirect/cookies не используются для images | assets/acquisition tests и документация ограничений |
| No-replace note-last publication, private durable journal | publication/recovery subprocess tests, idempotent receipt tests |
| Private storage policy, canonical aliases, startup/lifecycle enforcement | пять сценариев private-storage-boundaries в acceptance.md |
| Git conflict gate, явное разрешение, обычный sync | native integration; пользовательские пять sync в каждом браузере |
| Отдельный dist/ZIP, лицензии, Go embed не расширен | package.py, Python roundtrip/determinism tests, package.test.js, extracted ZIP smoke |
| Release только после проверок | read-only build проверяет extension/Go; publish зависит от build и проверяет семь архивов/checksums |

Proposal/design согласуются с protocol-v1, ручной доставкой, ограничениями
декодирования, private-storage policy и recovery. `assets`/dot-prefixed назначения
заметок запрещены на любой глубине; attachment storage `assets/images/` разрешён.
Сервер не получает изображения по source URL. Старый Chrome 120 — заявленный
минимум manifest, а не реально проверенная в этой сессии версия.

## Ограничения приёмки

- Native loopback prompt в Chrome/Chromium отсутствует: подключение проверено,
  Deny/Allow не объявлено выполненным. Пользователь согласовал условную проверку
  только при появлении диалога; отсутствие сохранено как NOT PRESENT во всех трёх
  браузерах. Подключение подтверждено отдельно.
- Версии ОС и всех трёх браузеров ручного прогона подтверждены пользователем
  в manual-results-2026-11-20.md; совпадают с локальной инвентаризацией.
- Повторная автоматическая визуальная проверка исправленной вёрстки прошла в
  Chromium/Chrome; Vivaldi в автоматическом UI-стенде завис. Это не FAIL продукта
  и не PASS исправленной вёрстки в Vivaldi.
- CI workflow проверяется локально по синтаксису, порядку и тем же командам.
  Реальный GitHub Actions run требует push и здесь не выполняется.
- Исторические Git-state таймауты не стираются. Ручные пять последовательных sync
  во всех трёх браузерах прошли; надёжность aggressive 50 ms polling не заявляется.

Задача 9.2 закрыта после подтверждения версий и явного согласования условного
loopback prompt пользователем 2026-11-20. Функциональные требования не исключены;
проверка optional CDN Deny/Allow сохранена.


## Регрессии, найденные финальной проверкой Chromium 157

- Встроенный Google Hangouts service worker появлялся раньше product worker.
  Во всех пяти worker harness теперь выбирается точный `/worker.js` расширения
  и проверяется manifest.name; три regression tests прошли RED → GREEN.
- `Range.cloneContents()` в живом документе повторно загружал baseline-изображения
  во время Selection. Локальный fixture подтвердил реальные GET, не только CDP
  cache events. Захват теперь переносит точные границы Range в инертную копию
  документа до клонирования. Регрессионный тест прошёл RED → GREEN; проверки
  частичного текста, image-only selection, исходного DOM и выделения сохранены.
  `captureRequests === []` в браузерном тесте не ослаблялся.
- Standalone smoke использует `os.tmpdir()` при отсутствии явного override,
  не требует сохранённого между перезагрузками `/tmp/opencode`.

Изменения прошли независимое read-only review без оставшихся P1/P2 замечаний.

Итоговые команды, результаты и hash проверенного ZIP: [verification-2026-11-20.md](verification-2026-11-20.md).
