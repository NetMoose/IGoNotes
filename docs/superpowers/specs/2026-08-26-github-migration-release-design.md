# Миграция в GitHub и автоматизация релизов

## Цель

Перенести IGoNotes из GitVerse в публичный GitHub-репозиторий без потери истории, сделать `develop` основной веткой и настроить воспроизводимую публикацию кроссплатформенных релизов по Git-тегам.

## Состояние Перед Миграцией

- Основной remote `origin` указывает на `git@gitverse.ru:netmoose/IGoNotes.git`.
- В GitVerse находятся ветки `master`, `develop`, `pages` и `Refactor`.
- Текущая рабочая ветка `fix/status-bar-layout` ещё не опубликована.
- Тегов нет.
- `master` является предком `develop`; `develop` опережает её на 43 коммита.
- GitHub CLI авторизован под учётной записью `NetMoose` по SSH.
- Репозиторий `NetMoose/IGoNotes` на GitHub ещё не существует.
- Устаревший `.gitea/workflows/release.yml` не используется GitHub и не обеспечивает воспроизводимую frontend-сборку.

## Стратегия Миграции

1. Подготовить и проверить текущие изменения в `fix/status-bar-layout`.
2. Создать пустой публичный репозиторий `NetMoose/IGoNotes` без начального README, лицензии и `.gitignore`.
3. Добавить GitHub как временный remote `github`, не изменяя `origin` до завершения проверки.
4. Отправить ветки `master`, `develop`, `pages`, `Refactor` и `fix/status-bar-layout`.
5. Установить `develop` основной веткой GitHub-репозитория.
6. Сверить вершины веток и доступность истории на GitHub.
7. После успешной проверки удалить локальный GitVerse remote `origin` и переименовать `github` в `origin`.

Удалённый репозиторий GitVerse не удаляется. Он остаётся доступным как архив, но локальная рабочая копия больше не отправляет в него изменения.

## Обновление Зависимостей

Выполнить `npm audit fix` без `--force`. Ожидаемое совместимое обновление lock-файла:

- `postcss`: `8.5.19` -> `8.5.26`;
- `nanoid`: `3.3.16` -> `3.3.18`.

Верхнеуровневые диапазоны `web/package.json` не меняются. После обновления `npm audit` должен сообщать о нуле известных уязвимостей, а frontend должен собираться командой `npm run build`.

## GitHub Release Workflow

Удалить `.gitea/workflows/release.yml` и создать `.github/workflows/release.yml`.

Workflow запускается при push тегов, соответствующих `v*`, например `v1.0.0`. Job получает только необходимое разрешение `contents: write` для создания GitHub Release.

Шаги workflow:

1. Checkout тегированного коммита.
2. Установка Node.js 24 с npm-кешем по `web/package-lock.json`.
3. `npm ci` в каталоге `web`.
4. `npm audit --audit-level=high` в каталоге `web`.
5. `npm run build` в каталоге `web` для создания `web/dist`.
6. Установка версии Go из `go.mod`.
7. `go test ./...`.
8. Кросс-компиляция с `CGO_ENABLED=0` для шести целей:
   - `linux/amd64`;
   - `linux/arm64`;
   - `windows/amd64`;
   - `windows/arm64`;
   - `darwin/amd64`;
   - `darwin/arm64`.
9. Упаковка Linux и macOS бинарников в `igonotes-<тег>-<os>-<arch>.tar.gz`, Windows бинарников в `igonotes-<тег>-<os>-<arch>.zip`.
10. Создание `checksums.txt` с SHA-256 для всех архивов.
11. Создание GitHub Release командой `gh release create` с автоматически сформированными release notes и всеми архивами.

Workflow использует встроенный `${{ github.token }}`. Дополнительные пользовательские токены и secrets не требуются.

## Документация Проекта

Обновить `README.md`, `AGENTS.md`, `docs/developer.md` и `site/docs/developer.md`: GitHub Actions становится системой сборки и публикации релизов, а релизным триггером являются теги `v*`. URL сайта документации в `site/_config.yml` не меняется до отдельной настройки GitHub Pages.

## Ошибки И Ограничения

- Ошибка установки зависимостей, аудит уровня `high` или `critical`, frontend-сборка, Go-тесты, любая целевая сборка либо публикация останавливают workflow.
- Уязвимости уровня `moderate` и ниже видны в отчёте, но не блокируют релиз.
- Каталог `web/dist` остаётся сгенерированным и не добавляется в Git.
- Настройка GitHub Pages не входит в миграцию; ветка `pages` только переносится.
- Защита веток и обязательные review rules не входят в эту задачу.
- Репозиторий GitVerse не удаляется автоматически.

## Проверка

- `npm audit` сообщает `0 vulnerabilities`.
- `npm ci` и `npm run build` проходят с обновлённым lock-файлом.
- `go test ./...` проходит.
- Все шесть кроссплатформенных бинарников собираются с подготовленным `web/dist`.
- В GitHub присутствуют все пять переносимых веток с совпадающими commit SHA.
- Основной веткой GitHub является `develop`.
- Локальный `origin` после проверки указывает на `git@github.com:NetMoose/IGoNotes.git`.
- Workflow доступен в GitHub Actions и настроен на теги `v*`; фактическая публикация проверяется при создании первого запланированного релизного тега.
