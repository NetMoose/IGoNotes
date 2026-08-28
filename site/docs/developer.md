---
layout: default
---

# Руководство разработчика IGoNotes

## Технологии

- Go 1.26+ и `net/http`.
- SQLite через `modernc.org/sqlite`.
- Svelte 5, Vite 8, Tailwind CSS 4, CodeMirror 6 и `marked`.
- Node.js 24 для текущего frontend toolchain.

Git-синхронизация приложения не реализована; `go-git` в зависимости не входит.

## Структура проекта

- `cmd/api/`: точка входа, CLI, сервер и системные маршруты.
- `internal/handlers/`, `internal/service/`, `internal/repository/`, `internal/model/`: HTTP, бизнес-логика, SQLite и модели.
- `web/src/`: Svelte-компоненты, API wrappers и Vitest-тесты.
- `web/dist/`: результат сборки Vite.
- `web/embed.go`: пакет `web` встраивает `dist` директивой `//go:embed all:dist`.

## Запуск и сборка

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
./builds/igonotes
```

## Архитектура frontend

`web/src/App.svelte` без client-side router переключает состояния `loading`, `error`, `setup`, `editor` и `settings`. Редактор заблокирован до завершения обязательного трехшагового setup.

`web/src/lib/setup/SetupWizard.svelte`, `BaseForm.svelte` и `DirectoryField.svelte` реализуют создание или подключение первой базы, системный выбор каталога и ручной запасной вариант. `web/src/lib/settings/SettingsWorkspace.svelte` и `BaseCard.svelte` отвечают за полноэкранное добавление, подключение, переименование, изменение пути, забывание и переключение баз без перезапуска.

Перед настройками и переключением ожидающие загрузки и изменения заметки сохраняются; ошибка блокирует переход. Забывание изменяет только конфигурацию и не удаляет файлы.

Общий `web/src/lib/api.js` предоставляет именованные wrappers и типизированный `ApiError` с полями `status`, `code`, `message` и `field`. Vitest 4, jsdom и Testing Library запускают component/module tests из `web/src/**/*.test.js`, которые покрывают `App`, редактор, setup, настройки и API.

## REST API

| Метод | Путь | Описание |
|:---|:---|:---|
| GET | `/api/notes` | Получить дерево заметок |
| POST | `/api/notes` | Создать заметку или папку |
| GET | `/api/note?id=...` | Получить содержимое заметки |
| DELETE | `/api/note?id=...` | Удалить заметку или папку |
| POST | `/api/save` | Сохранить содержимое заметки |
| PUT | `/api/rename` | Переименовать заметку или папку |
| POST | `/api/sync` | Синхронизировать файловую систему и метаданные |
| GET | `/api/info` | Получить путь текущей базы |
| GET | `/api/raw?path=...` | Получить файл из текущей базы |
| POST | `/api/assets` | Загрузить изображение |
| GET/PUT | `/api/config` | Получить/сохранить конфигурацию |
| POST | `/api/setup` | Завершить первичную настройку с первой базой |
| POST/PUT/DELETE | `/api/bases` | Добавить, изменить или забыть базу |
| POST | `/api/bases/switch` | Переключить активную базу |
| POST | `/api/system/select-directory` | Открыть системный выбор каталога |

Note API, кроме `/api/info`, недоступен до завершения setup. API защищен проверкой локального origin.

## Хранение данных

Markdown и `assets/images/` находятся в каталогах баз. SQLite хранится в `~/.igonotes/metadata.db`, конфигурация в `<os.UserConfigDir()>/igonotes/config.json` или каталоге из `--config`.

## Добавление новой функции

1. Измените модель, сервис, handler и маршрут в `internal/`.
2. Добавьте wrapper в `web/src/lib/api.js`.
3. Реализуйте небольшой Svelte-компонент в `web/src/lib/` и подключите его к `App.svelte`.
4. Добавьте Go-тесты и Vitest component/module tests.
5. Запустите полную проверку.

## Тестирование

```bash
cd web
npm test
npm run test:watch
```

Из корня проекта:

```bash
go test -race ./...
go vet ./...
make all
```

Git-синхронизация заметок остается будущей возможностью; вкладка Git отключена.

## Работа с Git

Проект использует `develop` для разработки, `master` для стабильной версии и `pages` для Jekyll-документации.

## Сборка релиза

GitHub Actions собирает архивы для Linux, Windows и macOS на amd64 и arm64 и публикует release при отправке тега вида `v*`.
