# Подготовка v0.0.1-alpha.6

Дата проверки: 2026-10-20.

## Состояние кандидата

- Последний опубликованный prerelease: `v0.0.1-alpha.5`.
- Следующая версия: `v0.0.1-alpha.6`.
- Ветка разработки изменений: `feat/settings-about`.
- Базовый коммит: `30662c07bd22e660f55b23becf4c4d4acae5d24f`.
- `origin/develop` после fetch по-прежнему указывает на этот базовый коммит.
- Код новой вкладки и переноса настроек находится в рабочем дереве, включая новые файлы; релизного коммита ещё нет.
- Архивы ниже — локальный кандидат из рабочего дерева. Официальные архивы должен заново собрать tag-driven GitHub workflow после интеграции изменений и создания тега на итоговом коммите.

## Подготовленные файлы

- Release notes: `docs/releases/v0.0.1-alpha.6.md`.
- Каталог локальных архивов: `builds/v0.0.1-alpha.6/` (игнорируется Git).
- Четыре `tar.gz`: Linux и macOS, amd64 и arm64.
- Два `zip`: Windows, amd64 и arm64.
- `checksums.txt`: SHA-256 всех шести архивов, имена соответствуют release workflow.
- В каждом архиве единственный бинарник `igonotes` либо `igonotes.exe`.

## Проверки

Среда: Node.js `v24.20.0`, npm `12.0.2`, Go `1.26.5 linux/amd64`, Git `2.43.0`.

Все команды завершились успешно:

```bash
npm --prefix web ci
npm --prefix web run verify
npm --prefix web audit --audit-level=high
make test
make test-git
make test-race
make vet
make verify
IGONOTES_TEST_VERSION=v0.0.1-alpha.6 go test -ldflags='-X IGoNotes/internal/appinfo.Version=v0.0.1-alpha.6' ./internal/handlers -run '^TestNoteHandlerGetInfoIncludesApplication$' -count=1
git diff --check
```

- Frontend: 508 тестов в 31 файле, production build выполнен.
- Git integration обязательна в `make test-git`, используется локальный Git без внешних remotes.
- Полный Go race detector прошёл в `make test-race` и составе `make verify`.
- Все шесть бинарников собраны с `CGO_ENABLED=0`, `-trimpath` и `-ldflags="-s -w -X IGoNotes/internal/appinfo.Version=v0.0.1-alpha.6"`.
- `sha256sum --check checksums.txt` проверил все шесть архивов; содержимое архивов проверено отдельно.
- Linux amd64-бинарник распакован из подготовленного архива и запущен с изолированными HOME и каталогом конфигурации. `GET /api/info` подтвердил `app_name: IGoNotes`, `app_version: v0.0.1-alpha.6` и путь автоматически созданной default-базы внутри временного HOME. Процесс корректно остановлен.

Сохраняются ранее известные нефатальные сообщения: два moderate advisory frontend-зависимостей, предупреждения double unmount в Modal-тестах и крупного основного Vite chunk. Проверка audit с релизным порогом high прошла.

## Следующий этап выпуска

Для публикации требуется зафиксировать изменения, интегрировать их в `develop`, дождаться CI итогового коммита и отправить annotated tag `v0.0.1-alpha.6` на этот коммит. Отправка тега запускает workflow сборки и публикации prerelease. После workflow нужно проверить опубликованные архивы и контрольные суммы и применить подготовленные release notes.

В рамках подготовки коммиты, push, merge и публикация не выполнялись.
