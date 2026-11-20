# Web Clipper: протокол ручной приёмки Chrome / Chromium

## 1. Назначение и результат

Протокол закрывает пользовательскую часть задачи **9.2** change
`add-chromium-web-clipper`. Выполняется человеком в **обычной графической сессии**
с работающим системным выбором каталога. Chrome и Chromium проверяются отдельно,
в новых профилях, на одной и той же сборке. Ориентир времени — 60–90 минут на
браузер; диагностические повторы Git могут потребовать больше времени.

Документ — инструкция, **не отчёт о выполнении**. Скопируйте
[шаблон отчёта](manual-report-template.md) в отдельный файл для каждого браузера.
Предыдущие автоматизированные headed-результаты находятся в
[acceptance.md](acceptance.md); они не заменяют действия из этого протокола.

Статусы каждого случая:

- **PASS** — выполнены все действия и наблюдались все ожидаемые результаты.
- **FAIL** — фактический результат противоречит ожидаемому; сохранить детали.
- **BLOCKED** — выполнить мешает среда или предыдущая ошибка.
- **NOT RUN** — случай не выполнялся, в том числе не удалось попасть в нужную
  фазу прерывания.
- **NOT PRESENT** — только для условного browser prompt: диалог не появился.
  Это наблюдение, **не PASS проверки Deny/Allow**.

При FAIL запишите его до повторного запуска. Последующий PASS не стирает сбой.
Не отмечайте 9.2 выполненной автоматически: результаты, отсутствие prompt и
оставшиеся ограничения должны пройти итоговый разбор.

### Что здесь проверяется вручную

Установка через системный chooser, настоящий popup и browser prompts,
офлайн-работа, адресация и видимость заметки, сохранность открытого редактора,
прерывания и пользовательский Git flow. Детальная серверная валидация файлов,
миграции, гонки публикации, crash boundaries и лимиты остаются покрытыми
автоматическими тестами; соответствие сценариям — в `acceptance.md`.

## 2. Подготовка стенда

### 2.1. Необходимые условия

- Linux desktop с доступным Chrome и Chromium; не Xvfb, не headless.
- Node.js **24**, Go **1.26+**, native Git **2.28+**, Python 3 для подготовки
  тестовых файлов; зависимости установлены до начала офлайн-сценариев.
- Свободны порты **19000** (IGoNotes) и **19491** (страница и картинки).
- Никаких рабочих баз/профилей: ниже создаются отдельные HOME, конфигурация,
  SQLite, базы и browser profile. Не запускайте для стенда `service install`.

Команды ниже — POSIX shell, из корня репозитория. В одном терминале:

```sh
node --version
go version
git --version
python3 --version
git rev-parse HEAD
git status --short
make all
npm --prefix extensions/webclipper ci
npm --prefix extensions/webclipper run verify
```

Ожидание: Node `v24.*`, команды сборки/verify завершаются кодом 0. В отчёт
перенести commit и список незакоммиченных изменений: один commit недостаточен
для идентификации сборки с dirty tree. После сборки не менять исходники/dist
между двумя браузерами. Если поменяли — начинать отчёт новой сборки.

Для **каждого браузера** создайте отдельный стенд в этом же терминале:

```sh
REPO="$PWD"
RUN="$(mktemp -d "${TMPDIR:-/tmp}/igonotes-manual.XXXXXXXX")"
export REPO RUN
mkdir -p "$RUN/home" "$RUN/config" "$RUN/base-active" \
  "$RUN/base-target/research/articles" "$RUN/evidence"
python3 - <<'PY'
import os
from pathlib import Path
r = Path(os.environ['RUN'])
(r/'base-active/keep-open.md').write_text('# Keep open\n\nACTIVE ORIGINAL\n', encoding='utf-8')
(r/'base-target/research/articles/existing.md').write_text('EXISTING: do not replace\n', encoding='utf-8')
for folder in ['.hidden', 'assets', 'nested/.hidden', 'nested/ASSETS']:
    (r/'base-target'/folder).mkdir(parents=True, exist_ok=True)
(r/'large-body.txt').write_text('# Interrupted upload\n\n' + 'manual upload line\n' * 150000, encoding='utf-8')
print('Stand:', r)
print('Large Markdown bytes:', (r/'large-body.txt').stat().st_size)
PY
```

Скопируйте значение `RUN` в отчёт. Дополнительные терминалы должны использовать
**этот же абсолютный путь**, а не новый `mktemp`. В каждом дополнительном
терминале присвойте `RUN` скопированное значение и выполните `export RUN`, прежде
чем запускать Python-блоки: они читают путь из окружения.

### 2.2. Сервер приложения и fixture

В первом терминале запустить сервер на переднем плане:

```sh
HOME="$RUN/home" XDG_CONFIG_HOME="$RUN/config" \
  "$REPO/builds/igonotes" --config "$RUN/config/igonotes" \
  --port 19000 --no-browser
```

Не экспортируйте подменённый HOME для всего desktop/browser. В этом запуске
SQLite/staging находятся под `$RUN/home/.igonotes`, конфигурация — отдельно от
корней заметок. Для обычного останова нажимайте Ctrl-C именно в этом терминале;
для рестарта повторяйте **ту же команду с тем же RUN и портом**.

Во втором терминале, из корня репозитория:

```sh
node extensions/webclipper/tests/browser/serve-fixtures.js
```

Ожидание: `Local manual fixture: http://127.0.0.1:19491/`.
Fixture слушает только loopback, раздаёт `/pixel.gif`, `/missing.gif` (404),
`/redirect` (302 на CDN). Он не запускает IGoNotes и не создаёт токены.

### 2.3. Обычный браузер с отдельным профилем

Запускать **один** из вариантов в третьем терминале; `RUN` — путь из §2.1:

```sh
google-chrome --user-data-dir="$RUN/profile" --no-first-run \
  --no-default-browser-check --no-proxy-server \
  '--host-resolver-rules=MAP images.example.test 127.0.0.1, MAP cdn.example.test 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'
```

```sh
chromium --user-data-dir="$RUN/profile" --no-first-run \
  --no-default-browser-check --no-proxy-server \
  '--host-resolver-rules=MAP images.example.test 127.0.0.1, MAP cdn.example.test 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'
```

Если исполняемый файл называется иначе, укажите его абсолютный путь и запишите
это в отчёт. Chrome должен быть **официальным Chrome**, а не вторым запуском
Chromium под другим именем. Сверьте `chrome://version`: версию, ОС, профиль и
командную строку. Путь профиля должен быть `$RUN/profile`. Не входить в аккаунт
и не включать browser sync. Сохранить скриншот версии.

Не добавлять `--load-extension`, remote debugging, `--no-sandbox`, отключение
web security или автоматическую выдачу permissions. Расширение устанавливается
только через реальный UI. Имена `.test` нужны, потому что буквальные loopback
адреса запрещены как источники изображений; это локальный fixture, не проверка
DNS-rebinding защиты. Правило `MAP * ~NOTFOUND` исключает внешние сайты, но не
подменяет наблюдение запросов самого расширения.

Откройте `http://127.0.0.1:19491/`: видны заголовок, длинный абзац и две
маленькие картинки. Их загрузка **самой страницей** не считается запросом
расширения и не означает, что расширению выданы image permissions.

### 2.4. Создание баз и наблюдение

1. Открыть `http://127.0.0.1:19000/`, пройти мастер: подключить существующий
   каталог `$RUN/base-active`, назвать базу **active**.
2. В **Настройки → Базы заметок → Добавить базу** подключить существующий
   `$RUN/base-target`, назвать **target**. Если добавление переключило базу,
   явно вернуться в **active**.
3. Открыть `keep-open.md`. Проверить `ACTIVE ORIGINAL` и путь active в статус-баре.
4. Для сетевых проверок открыть DevTools именно **вкладки редактора расширения**
   (после установки): Network → **Preserve log**, без фильтра ошибок. Применять
   фильтры `clipper` / `example.test` по конкретному случаю.
5. Для контроля дерева и редактора оставить приложение открытым в другой вкладке.
   Не обновлять эту вкладку во время M08/M09: это скрыло бы принудительный reload.

Для снимков очередности записывать время клика и наблюдения. В evidence не
помещать токен, Authorization, полный HAR или приватный browser profile.
У Network-скриншотов скрывать заголовки авторизации. Секрет выдаётся один раз:
после копирования скрыть его в UI, затем делать снимки.

## 3. Установка, capture и подключение

### M01. Load unpacked через системный chooser

1. Открыть `chrome://extensions`, включить **Developer mode / Режим разработчика**.
2. Нажать **Load unpacked / Загрузить распакованное расширение**.
3. В **системном окне выбора каталога** перейти в
   `$REPO/extensions/webclipper/dist` и подтвердить выбор каталога.
4. Проверить карточку **IGoNotes Web Clipper**, записать extension ID.
   Открыть Details; проверить отсутствие startup/CSP ошибок. Если есть Errors,
   сохранить текст, не очищать до записи FAIL.
5. Через меню «пазл» закрепить расширение на панели. Нажать настоящую иконку.

**Ожидание:** chooser действительно открылся, выбран dist, popup содержит
**Article**, **Selection**, **Open draft editor**. Нативный chooser и карточку
зафиксировать отдельно. CDP-установка не даёт PASS M01. Если chooser не работает,
записать BLOCKED с окружением и сообщением; не подменять этот шаг загрузочным флагом.

### M02. Article без сервера и без изменения страницы

1. Остановить IGoNotes Ctrl-C; fixture оставить работающим.
2. Открыть fixture `/`, запомнить положение страницы. Через toolbar → **Article**.
3. Дождаться **Saved locally** и отдельной вкладки редактора.
4. Проверить Title, Markdown, Source URL и Captured at; в Markdown присутствует
   статья и image URLs, а не служебный UI страницы. Картинки пока не Stored locally.
5. Вернуться к источнику: текст/картинки не удалены, прокрутка не сброшена захватом.
6. Закрыть исходную вкладку, перезагрузить редактор расширения.

**Ожидание:** сохранённый draft открывается с тем же ID (параметр `draft` в URL),
источником и временем; нет попытки отправки в неработающий Go. Capture не запускает
Prepare. Без DevTools до capture отсутствие сетевых запросов при capture не
объявлять отдельно доказанным: это также проверяется автоматическим smoke.

### M03. Selection и ошибки capture

1. Снова открыть fixture. Мышью выделить **только первый короткий абзац**.
2. Через настоящую иконку расширения выбрать **Selection**.
3. Проверить, что новый draft содержит выбранный абзац, но не длинный текст
   `This offline fixture...`; выделение и источник не разрушены.
4. Снять выделение, повторить Selection: видна ошибка про непустое выделение,
   новый пустой draft не появляется.
5. На `chrome://extensions` открыть popup → Article: явная ошибка неподдерживаемой
   страницы, без создания пустого draft и подмены режима.

**Ожидание:** оба отрицательных результата сохраняют ранее созданные drafts.

### M04. Редактирование, reopen и Markdown download

1. В draft M03 изменить Title на `Ручная проверка "Unicode" 📝`, Filename на
   `selection-manual.md`; дописать в Markdown `EDIT-SAVED-01`.
2. Нажать **Save**, дождаться **Saved locally**, записать source/time.
3. Закрыть и открыть редактор через popup → Open draft editor, выбрать draft.
4. Проверить все сохранённые поля. Нажать **Download Markdown**, открыть файл
   текстовым редактором.

**Ожидание:** последняя правка сохранена, source/time не заменены временем reopen;
скачан Markdown с тремя YAML-полями `title`, `source`, `clipped_at`, Unicode и
текстом. Скачивание не отправляет импорт и не является экспортом картинок.

### M05. Токен, loopback и custom port

1. Запустить Go той же командой из §2.2. В приложении:
   **Настройки → Веб-клиппер → Название подключения** = `manual-browser`,
   **Создать подключение**. Скопировать одноразовый **Секрет подключения**.
2. В расширении открыть **Connection settings**: Endpoint =
   `http://127.0.0.1:19000`, Connection token = выданный секрет.
3. Очистить Network, нажать **Save connection**. Наблюдать 10 секунд.
4. Нажать **Test connection**. Если браузер показывает local-network/loopback
   prompt — сначала **Deny**, записать текст/кнопки, проверить ошибку и сохранность
   draft; разрешить повторный запрос через штатные site/extension settings
   браузера, повторить Test и **Allow**. Записать точный путь изменения настройки.
5. Если prompt не появился, записать **NOT PRESENT**, версию и успешность
   подключения отдельно. Не имитировать его optional CDN permission.
6. Проверить сообщение `Connected: protocol v1 is supported.`
7. Сменить Endpoint на `http://localhost:19000`, Save → Test. Повторить запись
   наличия/отсутствия prompt для этого имени. Вернуть и сохранить исходный
   `http://127.0.0.1:19000` для остальных случаев.
8. Закрыть/открыть редактор: сохранённые настройки доступны. Проверить невалидный
   Endpoint `http://example.test:19000` через Test: ошибка до запроса с токеном;
   вернуть правильный endpoint. Токен не копировать в адресную строку.

**Ожидание:** Save не делает HTTP-запрос; Test делает read-only discovery,
нет `POST .../imports`. Оба loopback имени и нестандартный порт работают.
Ошибка/отказ не удаляют drafts. **M05 connectivity** и **M05 native Deny/Allow**
имеют отдельные результаты; NOT PRESENT оставляет native-подслучай незакрытым
до решения по итогам приёмки.

## 4. Изображения и офлайн

### M06. Настоящий optional-host Deny → Allow

1. В новом article draft заменить Markdown на следующие строки, сохранить:

   ```markdown
   # Permission test
   ![one](http://images.example.test:19491/pixel.gif)
   ![same again](http://images.example.test:19491/pixel.gif)
   ```

2. Очистить Network в extension editor. Нажать **Prepare images**.
3. Сфотографировать настоящий browser prompt с названием расширения/хостом,
   нажать **Deny / Запретить**.
4. Проверить ошибку разрешения, сохранность текста/draft, отсутствие fetch
   картинки **из расширения**. Send не готов к работе.
5. Нажать **Retry images / grant access**, в появившемся prompt — **Allow**.
6. Дождаться завершения и **Stored locally**, без ошибок для двух вхождений.
7. В DevTools → Application → IndexedDB открыть БД расширения, store `assets`:
   убедиться, что есть Blob и hash; повторные ссылки используют один набор байтов.
   Не менять записи, не выполнять Console-команды выдачи разрешений.

**Ожидание:** host prompt появляется только после явной кнопки, запрашивает
конкретный host, а не все сайты. До разрешения нет extension image fetch;
после разрешения успешный Blob сохранён, а не только получен сетевой ответ.
Если prompt уже не появляется, проверить, не выдавалось ли разрешение ранее;
повторить в свежем профиле и записать новый run. Не считать заранее выданный
доступ тестом native Allow.

### M07. Второй host, 404, redirect и отдельный external fallback

1. В другом draft сохранить:

   ```markdown
   # Partial image test
   ![cdn](http://cdn.example.test:19491/pixel.gif)
   ![missing](http://cdn.example.test:19491/missing.gif)
   ![redirect](http://images.example.test:19491/redirect)
   ```

2. Нажать Prepare: для ранее не выданного CDN проверить настоящий Deny, затем
   Retry → Allow. Записать **весь список hosts** в prompt: он может включать
   уже встречавшийся images host вместе с CDN.
3. Проверить: `/pixel.gif` Stored locally; `/missing.gif` — 404;
   `/redirect` — сетевая/redirect ошибка. Непрозрачная ошибка fetch может не
   различать network/redirect; сам redirect не должен успешно загрузить картинку.
4. Load destinations → target → research/articles → Confirm destination.
   Send должен быть заблокирован из-за неразрешённых ошибок.
5. У missing и redirect отметить **Keep external reference**. До отдельного
   подтверждения Send по-прежнему заблокирован.
6. Нажать **Confirm external references**. Видно
   `This material is not fully offline / uses external images.`
7. Для проверки сброса согласия изменить Markdown и Save: external confirmation
   снова требуется. Повторить выбор и подтверждение, назначить Filename
   `external-manual.md`, явно подтвердить назначение и нажать Send.

**Ожидание:** Import confirmed показывает target/path и строковые warnings;
в файле успешное изображение локальное, missing/redirect остаются согласованными
HTTP URLs. Материал не называется полностью автономным. До подтверждения не
было import POST. Сохранить warnings и снимок блокировки Send.

### M08. Полученные байты переживают browser restart и отключение источника

1. Использовать **не отправленный** готовый draft M06. Записать ID, Markdown,
   Stored locally и размер Blob в IndexedDB.
2. Закрыть исходные страницы. В `chrome://extensions` → Details → Site access
   отозвать ранее выданный optional image host, если UI этой версии позволяет.
   Записать точные действия; не удалять/не переустанавливать расширение. Если
   выборочного отзыва нет, отметить этот подслучай BLOCKED, основной продолжить.
3. Остановить fixture и Go через Ctrl-C в их терминалах.
4. Полностью выйти из браузера через меню **Exit / Выход**, не только закрыть
   вкладку редактора. Убедиться, что команда браузера завершилась; иначе случай
   полного process restart не засчитан.
5. Запустить тот же браузер командой §2.3 с **тем же profile**. Не вызывать
   Load unpacked заново. Через toolbar открыть сохранённый draft.
6. Проверить ID, Markdown, Stored locally, Blob/размер. Открыть Network,
   перезагрузить editor, наблюдать 10 секунд.

**Ожидание:** расширение само осталось установленным, данные и готовые bytes
сохранены, нет автоматических fetch картинки, discovery, status GET или POST.
Не нажимать Prepare/Check/Send при этой проверке. Вернуть Go и fixture теми же
командами; само восстановление серверов также не вызывает отправку. Дополнительно
проверить, какие host permissions сохранились/были отозваны после обычного restart.

## 5. Назначение, редактор и ошибки отправки

### M09. Неактивная база не меняет открытый редактор

1. В приложении явно выбрать active, открыть `keep-open.md`, записать
   `ACTIVE ORIGINAL`, текущий путь, выбранный узел и положение курсора.
2. В расширении подготовить новый draft без изображений: Markdown =
   `INACTIVE IMPORT M09`, Filename = `inactive-manual.md`; нажать Prepare images,
   чтобы UI проверил отсутствие image-заданий.
3. Load destinations: нет автоматически выбранной базы/папки. Выбрать target.
   В списке есть `(root)`, `research`, `research/articles`, но нет `.hidden`,
   `assets`, `nested/.hidden`, `nested/ASSETS` и их потомков.
4. Выбрать research/articles → Confirm destination → Send.
5. **Не переключая/не обновляя приложение**, проверить его редактор, выбранный
   узел, курсор и статус-бар.
6. Файловым менеджером проверить target/research/articles/inactive-manual.md.
   Затем **явно** переключить приложение на target: заметка видна и открывается.

**Ожидание:** receipt указывает target/research/articles; active не стала target,
открытый текст/курсор не сброшены, файл не появился в active. До явного переключения
не было принудительной перезагрузки открытой заметки.

### M10. Активный импорт виден в дереве и не перезагружает другую заметку

1. Вернуть active, открыть `keep-open.md`, поставить курсор в конец.
2. Напечатать строку `OPEN EDITOR M10`; дождаться автосохранения. Не менять
   выделенный файл, не обновлять страницу и не пользоваться кнопкой tree sync.
3. Отправить из расширения новый текст `ACTIVE IMPORT M10`, Filename =
   `active-manual.md`, назначение active/(root).
4. После receipt сразу проверить текст, курсор и путь открытого `keep-open.md`.
5. Открыть DevTools **приложения** → Network, затем в Console выполнить только
   read-only запрос дерева:

   ```js
   await fetch('/api/notes').then(r => r.json())
   ```

6. В результате найти `active-manual.md`; закрыть DevTools, проверить, что
   открытая заметка не была заменена импортированной.

**Ожидание:** следующий GET дерева уже видит импорт без POST `/api/sync`;
редактор сохранил последнюю правку и курсор. Самостоятельное визуальное обновление
дерева без запроса не требуется; Send не должен автоматически открывать новый файл.
Это дополняет M09: проверяется реально открытая заметка, а не пустое окно приложения.

### M11. Вложенные локальные изображения в preview

1. Создать draft с двумя ссылками на pixel.gif хоста images, Prepare → Stored
   locally, Filename = `nested-images-manual.md`.
2. Явно выбрать target/research/articles, Confirm → Send.
3. Открыть файл текстовым редактором: пути вида
   `../../assets/images/clip-<import_id>-<sha256>.gif`.
4. Убедиться, что файл изображения есть в target/assets/images, повторная ссылка
   не создала второй одинаковый asset для этого import.
5. В приложении **явно** выбрать target, открыть заметку и preview. В Network
   приложения найти `/api/raw?path=assets...`: статус 200; оба изображения видны
   (это маленькие 1×1 GIF, поэтому проверить также элемент/размер в DevTools).
6. Остановить fixture, обновить preview: картинки по-прежнему приходят от Go.

**Ожидание:** нет обращения к source/CDN для локализованных изображений;
вложенные `../` нормализованы внутри базы. Подпапка не создаётся автоматически.

### M12. Совпадение имени и устаревшее назначение

1. Попытаться отправить новый текст в target/research/articles с Filename
   `existing.md` (исходный файл создан в §2.1).
2. Проверить `note_conflict`, сохранность draft и точное содержимое исходного
   файла `EXISTING: do not replace`. Изменить имя на `collision-retry.md`, заново
   Load/выбрать/Confirm → Send: успех под новым именем.
3. В другом draft выбрать/подтвердить target/research/articles, **не отправлять**.
   Перезапустить Go с тем же HOME/config/портом. Нажать Send.
4. Проверить требование обновить назначение; без дополнительных кликов не должно
   создаться заметки или автоматически выбираться active.
5. Явно Load → target → research/articles → Confirm → Send: успех.

**Ожидание:** чужой файл не перезаписан; destination после restart не обновляется
молча. Для уже существующей frozen attempt пользоваться M14, не менять её ID/поля.

### M13. Server offline, отзыв токена и переподключение

1. На новом draft подтвердить назначение, остановить Go, нажать Send.
2. Проверить ошибку и сохранность draft. Вернуть Go, **ничего не нажимать**
   10 секунд: не должно быть import POST. Новый editable draft требует свежего
   подтверждения назначения; frozen attempt — только явных Check/Retry.
3. В приложении отозвать тестовое подключение через **Отозвать**. В расширении
   нажать Test connection: ошибка авторизации; drafts не удалены.
4. Создать новый секрет, сохранить его на **том же endpoint**, Test connection.
5. Явно подтвердить/отправить editable draft либо проверить frozen attempt
   согласно M14. Clear connection затем не должно удалять другие drafts.
   После проверки Clear вернуть endpoint и новый токен, Save → Test: подключение
   понадобится следующим сценариям.

**Ожидание:** ни reconnect, ни сохранение настроек, ни online не начинают Send;
отозванный токен больше не работает, замена токена не означает смену сервера.

## 6. Прерывание реальной отправки

### M14. Прерванный upload и restart без нового ID

Этот случай зависит от попадания в нужную фазу. **Прерывание до POST — не проверка
сохранённой posting attempt**. Если POST успел завершиться и Import confirmed
уже показан, записать NOT RUN для interrupted-подслучая и повторить с новым draft.

1. В новом текстовом draft вставить **весь текст** `$RUN/large-body.txt` через
   обычный текстовый редактор/буфер обмена. Filename = `interrupted-manual.md`.
   Save → Saved locally; размер меньше 5 MiB. Prepare images, явно подтвердить
   target/research/articles.
2. DevTools extension editor → Network: Preserve log. Установить **Slow 3G**
   (или профиль с upload 50 kbit/s, latency 200 ms), убрать фильтр запросов.
3. Нажать Send. Дождаться **POST `/api/clipper/v1/imports` в pending**, а не только
   предварительных GET. В Application → IndexedDB → attempts записать import ID
   и состояние, не редактируя записи. Можно также записать ID из immutable UI,
   если он уже появился.
4. Пока POST pending, остановить Go Ctrl-C и полностью выйти из браузера.
   Записать момент останова и существовал ли конечный файл в target.
5. Запустить Go тем же способом и браузер с тем же профилем. Открыть draft,
   отключить throttling (No throttling). Ничего не нажимать 10 секунд; записать
   отсутствие автоматических POST/status GET. Проверить сохранность ID/текста.
6. Frozen attempt должна остаться read-only. **Check attempt** — явное чтение
   статуса. Если succeeded: receipt и cleanup, повторных файлов нет. Если unknown:
   нажать **Check / retry exact attempt**; только исходный ID/payload.
7. После Go restart незавершённый неизвестный запрос может получить
   `clipper_destination_changed`: draft сохраняется, затем пользователь явно
   загружает/подтверждает назначение и выполняет новую попытку. Это ожидаемая
   ветка, не разрешение менять frozen snapshot вручную.
8. Если pending/recovery_required — сохранить состояние; проверить в приложении
   **Настройки → Веб-клиппер** незавершённые импорты. Не нажимать abandon ради
   зелёного результата и не создавать копию под другим ID.
9. Проверить target: не более одной заметки с этим содержимым/назначением,
   нет перезаписи существующего файла. Записать последовательность ID/HTTP-кодов.

**Ожидание:** подтверждённо сохранённая attempt пережила полный browser/Go restart,
без автоматической отправки, ретаргетинга и дубликатов. Если оказался только
editable offline-preflight draft — M13 покрыт, M14 остаётся NOT RUN.
Наличие succeeded после реального прерывания клиента даёт дополнительное
наблюдение восстановления receipt, но случайное попадание в это окно **не заменяет**
детерминированный lost-success-response тест автоматического harness.

## 7. Настоящий Git-конфликт и обновление статуса

### M15. Подготовка локального remote и Git-подключение

Все команды только для `$RUN/base-target`, не для репозитория исходников.
Go работает; autosync в UI будет выключен. В дополнительном терминале установить
тот же `RUN`, затем:

```sh
git init --bare --initial-branch=main "$RUN/remote.git"
git -C "$RUN/base-target" init --initial-branch=main
git -C "$RUN/base-target" config --local user.name 'Manual Acceptance'
git -C "$RUN/base-target" config --local user.email 'acceptance@example.invalid'
python3 - <<'PY'
import os
from pathlib import Path
(Path(os.environ['RUN'])/'base-target/conflict.md').write_text('baseline\n', encoding='utf-8')
PY
git -C "$RUN/base-target" add .
git -C "$RUN/base-target" -c user.name='Manual Acceptance' \
  -c user.email='acceptance@example.invalid' commit -m 'manual baseline'
git -C "$RUN/base-target" remote add origin "$RUN/remote.git"
git -C "$RUN/base-target" push -u origin main
git clone "$RUN/remote.git" "$RUN/other"
```

Локальная identity в base-target нужна не только первому CLI commit, но и
последующим commit/merge приложения с изолированным HOME. Эти две команды меняют
только конфигурацию одноразового тестового репозитория, не глобальный Git config.

1. В приложении **Настройки → Git-синхронизация**, для target открыть подключение.
   URL репозитория — абсолютный `$RUN/remote.git`, ветка main,
   **Только вручную**, шаблон `Manual {{base}} {{count}}`.
2. Проверить репозиторий/ветку, просмотреть изменения, сохранить Git-настройки.
3. Сразу наблюдать статус/Network приложения. Принятие операции в очередь
   (HTTP 202) **не является** ready. Дождаться terminal ready, максимум 60 секунд
   для этого локального стенда. При превышении — FAIL с последним state/stage/
   error, не произвольно увеличивать время.

**Ожидание:** конечный ready, без `500 internal_error` на GET статуса,
без error/paused/needs_reconnect. Записать время принятия и время terminal state.

### M16. Конфликт блокирует импорт, UI-разрешение позволяет явный retry

1. В дополнительном терминале создать расхождение одного файла:

   ```sh
   python3 - <<'PY'
   import os
   from pathlib import Path
   r = Path(os.environ['RUN'])
   (r/'other/conflict.md').write_text('REMOTE VERSION\n', encoding='utf-8')
   (r/'base-target/conflict.md').write_text('LOCAL VERSION\n', encoding='utf-8')
   PY
   git -C "$RUN/other" add conflict.md
   git -C "$RUN/other" -c user.name='Manual Remote' \
     -c user.email='remote@example.invalid' commit -m 'remote conflict'
   git -C "$RUN/other" push origin main
   ```

2. В приложении для target нажать **Синхронизировать сейчас**. Дождаться
   `conflict` (не только HTTP 202). Записать Network/последний state.
3. До Send записать состояние/байты worktree:

   ```sh
   git -C "$RUN/base-target" status --porcelain=v1
   python3 - <<'PY'
   import hashlib, os
   from pathlib import Path
   r = Path(os.environ['RUN'])/'base-target'
   rows = [(p.relative_to(r).as_posix(), hashlib.sha256(p.read_bytes()).hexdigest())
           for p in sorted(r.rglob('*')) if p.is_file() and '.git' not in p.relative_to(r).parts]
   print(*rows, sep='\n')
   PY
   ```

4. В расширении новый текст `GIT GATE M16`, Filename = `after-conflict.md`,
   Prepare, явное target/research/articles → Confirm → Send.
5. В Network POST возвращает **409 git_conflict_pending**. Draft/attempt остаётся,
   нет Import confirmed. Повторить команды шага 3: worktree status и hashes
   прежние, файла after-conflict.md нет.
6. В приложении **явно переключиться на target**, открыть workspace конфликтов;
   для conflict.md выбрать **Оставить версию на этом устройстве** →
   **Применить решение** → **Завершить слияние**. Дождаться ready по настоящему
   terminal state, записать длительность/ошибки. Не делать git reset/ручной commit
   вместо пользовательского resolver.
7. В расширении нажать **Check / retry exact attempt**. Записать исходный ID и
   фактический результат. При штатном успехе Import confirmed, одна заметка,
   `conflict.md` содержит `LOCAL VERSION`. Если сервер вернул окончательный
   rejected, не объявлять PASS exact-retry: сохранить ответ и состояние для разбора.

**Ожидание:** conflict gate не меняет worktree; разрешение только явное;
импорт возобновляется явным действием без обхода gate и замены immutable ID.

### M17. Обычный sync и повторяемость Git-status

1. Для target нажать **Синхронизировать сейчас**. Сразу наблюдать Network и статус,
   не вставлять ручную задержку перед началом наблюдения. Дождаться ready ≤60 с.
2. Проверить remote независимо:

   ```sh
   git -C "$RUN/other" fetch origin
   git -C "$RUN/other" show origin/main:research/articles/after-conflict.md
   ```

3. Повторить обычный sync **ещё четыре раза**, каждый раз только после terminal
   предыдущего. Для каждого записать queue/terminal время, state/stage и коды
   ответов GET. Не запускать второй sync, пока предыдущий ещё syncing.
4. При timeout/500 сначала сохранить последний JSON `/api/git/status?base=target`
   из Network (или открыть этот URL в приложении), консоль и лог Go. Только потом
   пробовать повтор. Наличие статуса ready до первого клика не доказывает, что
   новая queued operation завершилась: учитывать operation ID, если он доступен.

**Ожидание:** note содержится в обычном Git remote; все пять операций завершаются
без подвисания/500/paused. Это пользовательская проверка повторяемости, **не**
доказательство надёжности polling каждые 50 ms. Два прежних таймаута из
`acceptance.md` остаются отдельными наблюдениями, пока их причина не установлена.

## 8. Завершение и передача результатов

1. Заполнить отчёт для текущего браузера; приложить скриншоты chooser, версии,
   настоящих prompts, ошибок/receipt, открытой заметки до/после импорта и Git.
2. Записать все FAIL/BLOCKED/NOT RUN/NOT PRESENT, даже если остальная часть прошла.
   Особо: M05 native loopback, M08 normal installation persistence, M09/M10 open
   editor preservation, M14 реально достигнутый POST, M17 таймауты.
3. Остановить только процессы этого стенда через их терминалы/браузерное Exit.
   Сохранить RUN до разбора спорных результатов. Не публиковать его profile,
   SQLite, конфигурацию и полный HAR вместе с отчётом.
4. Для второго браузера повторить §2 с **новым RUN**, тем же кодом/dist. Не
   переносить browser profile, image grants или секрет предыдущего прогона.
5. Передать два отчёта и evidence. Отдельно решить по NOT PRESENT loopback:
   найден поддерживаемый браузер с реальным prompt либо согласована применимость
   критерия. Отсутствие prompt нельзя молча превратить в PASS Deny/Allow.

Выполнение протокола не заменяет общую приёмку **9.4**, CI/distribution **9.3**,
сверку спецификаций **9.5** и review незакоммиченных исправлений. Никакие checkbox
OpenSpec этот документ сам не закрывает.

Справка по штатной установке и user-gesture permissions:
[Chrome: Load unpacked](https://developer.chrome.com/docs/extensions/get-started/tutorial/hello-world#load-unpacked),
[Chrome: optional permissions](https://developer.chrome.com/docs/extensions/reference/api/permissions).
