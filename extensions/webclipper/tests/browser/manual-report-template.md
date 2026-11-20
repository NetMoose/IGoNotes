# Web Clipper — отчёт ручной приёмки одного браузера

Статус документа: **пустой шаблон, проверки не выполнены**.
Порядок действий и значения статусов: [manual-acceptance.md](manual-acceptance.md).
Для Chrome и Chromium заполнить отдельные копии.

## Стенд

| Поле | Фактическое значение |
| --- | --- |
| Дата, время и часовой пояс | |
| Исполнитель | |
| ОС / desktop / display server | |
| Chrome или Chromium, полная версия | |
| Путь browser executable | |
| Node / Go / Git версии | |
| Commit | |
| Dirty tree: список изменений / ссылка на patch | |
| RUN / путь browser profile | |
| Extension ID / выбранный dist | |
| Endpoint / fixture port | |
| Установка через настоящий chooser | NOT RUN |
| Работал sandbox, отсутствовали pregrant/debug/CDP install flags | NOT RUN |
| Папка со скриншотами | |

Секрет подключения, Authorization, полный HAR и содержимое профиля не прикладывать.

## Результаты

В последнем столбце записать **факт**, а не копию ожидаемого результата.
Ссылки/имена скриншотов, HTTP-коды и ID попыток допустимы; секреты — нет.

| ID | Проверка | Статус | Фактическое наблюдение / evidence / issue |
| --- | --- | --- | --- |
| M01 | Настоящий Load unpacked chooser, установка, toolbar, CSP | NOT RUN | |
| M02 | Article без Go, сохранение и неизменность страницы | NOT RUN | |
| M03 | Selection, пустое выделение, restricted page | NOT RUN | |
| M04 | Edit / Save / reopen / Markdown download | NOT RUN | |
| M05-a | Save без сети, Test, 127.0.0.1:19000 | NOT RUN | |
| M05-b | localhost:19000 | NOT RUN | |
| M05-c | Native loopback Deny/Allow для каждого имени | NOT RUN | При отсутствии — NOT PRESENT, не PASS |
| M05-d | Сохранение настроек и invalid endpoint | NOT RUN | |
| M06 | Native image Deny → Retry → Allow, Blob/dedup | NOT RUN | |
| M07 | Второй host, 404/redirect, отдельный external consent, warnings | NOT RUN | |
| M08-a | Полный browser restart без повторного Load unpacked | NOT RUN | |
| M08-b | Offline cached bytes и отсутствие auto-fetch/send | NOT RUN | |
| M08-c | Выборочный отзыв host, сохранность cache и grants после restart | NOT RUN | |
| M09 | Inactive import: прежняя база, открытый текст/курсор | NOT RUN | |
| M10 | Active import: следующий GET дерева, без reload открытой заметки | NOT RUN | |
| M11 | Nested image paths, локальный preview без fixture | NOT RUN | |
| M12 | Collision без перезаписи, stale destination, explicit reconfirm | NOT RUN | |
| M13 | Offline / revoke / reconnect без automatic Send | NOT RUN | |
| M14 | Pending POST прерван, тот же ID после restart, manual recovery | NOT RUN | |
| M15 | Git setup и корректный terminal status | NOT RUN | |
| M16 | Реальный конфликт, gate без изменения файлов, UI resolve, exact retry | NOT RUN | |
| M17 | Обычный Git sync + пять последовательных циклов | NOT RUN | |

## Loopback prompt — отдельное наблюдение

| Endpoint | Был ли prompt | Текст / requested scope | Deny: результат | Allow/retry: результат |
| --- | --- | --- | --- | --- |
| http://127.0.0.1:19000 | NOT RUN | | | |
| http://localhost:19000 | NOT RUN | | | |

## Прерванная попытка M14

- Draft ID:
- Import ID перед прерыванием / после restart:
- POST действительно был pending, evidence:
- Момент останова / существовал ли конечный файл:
- Состояние при reopen, отсутствие автоматических запросов:
- Явные Check / Retry и ответы:
- Ветка: succeeded / unknown / destination_changed / pending / recovery_required:
- Конечные файлы, число копий:

## Git M15–M17

| Операция | Время queue / operation ID | Время terminal | State / stage | HTTP ошибки / evidence |
| --- | --- | --- | --- | --- |
| Initialize | | | | |
| Conflict sync | | | | |
| Complete merge | | | | |
| Ordinary sync 1 | | | | |
| Ordinary sync 2 | | | | |
| Ordinary sync 3 | | | | |
| Ordinary sync 4 | | | | |
| Ordinary sync 5 | | | | |

## Ошибки и повторы

Для каждого отклонения: ID случая, точные действия, ожидаемый/фактический результат,
время, evidence, последний state/stage/error. Оригинальный сбой не удалять при
успешном повторе; результат повтора записать отдельно.

## Итог

- PASS / FAIL / BLOCKED / NOT RUN / NOT PRESENT: число каждого статуса:
- Незакрытые критерии и необходимые действия:
- Стенд сохранён для диагностики / процессы остановлены:
- Решение о приёмке: **не принято**.
- Кто и когда разобрал результаты / согласовал применимость отсутствующих prompts:
