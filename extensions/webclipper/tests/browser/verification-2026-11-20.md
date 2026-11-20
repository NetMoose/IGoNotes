# Финальные проверки — 2026-11-20

Рабочая копия `feat/chromium-web-clipper`; HEAD до изменений `6e5bba4`.
Результаты относятся к незакоммиченным изменениям этой сессии.
Node 24.20.0, Go 1.26.5, Git 2.43.0, Linux Mint 22.3.

| Команда / проверка | Результат |
| --- | --- |
| `npm --prefix web ci` (prefer-offline, no audit) | PASS |
| `npm --prefix web run verify` | PASS, 35 файлов / 585 тестов и build |
| `npm --prefix extensions/webclipper ci` (prefer-offline, no audit) | PASS |
| `npm --prefix extensions/webclipper run verify` | PASS, 34 файла / 814 JS-тестов, 2 Python-теста и build |
| `make test` | PASS |
| `make test-git` | PASS, обязательный native Git |
| `make test-race` | PASS |
| `make vet` | PASS |
| `make verify` | PASS, повторные frontend/Go/race/vet |
| `npm --prefix extensions/webclipper run test:browser` на распакованном ZIP | PASS, 24 functional cases + 5 fault cases, Chromium 157.0.8103.78, sandbox enabled |
| `go test ./internal/docs -count=1` после последних правок docs | PASS |
| `openspec validate add-chromium-web-clipper --strict` | PASS |
| npm audit обоих пакетов, включая devDependencies | 0 vulnerabilities |
| `make clipper-package`, ZIP identical to browser-tested archive | PASS |
| YAML + bash syntax CI/release | PASS |
| Реальная checksum gate release с локальной заглушкой gh | valid PASS; missing/extra/tampered отклонены |
| Независимое read-only review | Нет оставшихся P1/P2 замечаний |

Первый browser run был FAIL: runner выбирал встроенный Google Hangouts worker.
После исправления выбора worker выявлен реальный повторный GET изображений при
Selection в live Range. Обе причины исправлены, regression tests RED → GREEN,
последующий полный browser run PASS. Эти первоначальные сбои не переименованы
в успешные результаты. Последние изменения после полного Go-прогона затронули
только отдельный extension/harness и документы; extension перепроверен полностью,
зеркала docs — отдельным Go-тестом.

Проверенный архив: `builds/igonotes-webclipper.zip`, manifest в корне, 25 файлов.
SHA-256: `dbaf3127c603d9dc1bbe97810bde187b06144ee80bde4969f782c0f75cd3a042`.
Распакованный архив побайтно совпадает с текущим dist. JSON browser report
подтверждает `processGroupsGone`, `profilesRemoved`, `listenersClosed` = true,
`errors = []`. Логи и redacted JSON: `/tmp/igonotes-final-20261120/` (временные).

Проверки после установки зависимостей выполнялись с offline npm/Go cache.
Внешняя сеть использовалась только для обязательного npm audit, исправленных
пакетов и документации Context7; browser fixtures и Git remotes — локальные.
Обновлён основной frontend toolchain; целевой override selector-parser указан
в developer docs. `npm audit fix --force` не использовался.

Реальный GitHub Actions run, публикация, push и merge не выполнялись. В CI
sandbox отключён явно только для disposable runner; локальный финальный browser
run использовал обычный sandbox. Ручные результаты и согласованное закрытие
9.2 — в [manual-results-2026-11-20.md](manual-results-2026-11-20.md),
контракты и все 56 сценариев — в [reconciliation.md](reconciliation.md).
