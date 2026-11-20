# PromUI

[English](README.md) · [Русский](README-ru.md)

Небольшой веб-интерфейс для управления файлами `file_sd` Prometheus,
рассчитанный на запуск рядом с Prometheus (например, на Docker-хосте,
куда смонтирован том `files_sd`).

PromUI — приложение, которое:

- читает текущую конфигурацию через **read-only** API
  (`/api/v1/status/config`), извлекает scrape-джобы с их
  `file_sd_configs` (глобы, `refresh_interval`) и другими параметрами;
- сопоставляет файлы с джобами по **реальным глобам** из конфигурации
  (семантика `path.Match` по путям относительно каталога files_sd),
  а не по эвристикам от префикса имени: файл отображается под каждым
  джобом, чьим глобам он подходит, а файлы, которые не читает ни один
  джоб, группируются в «Другие» (Others);
- показывает статусы целей из `/api/v1/targets` (scrape URL, здоровье,
  последняя ошибка, время последнего скрейпа);
- редактирует файлы `<job>_<id>.yml` (список групп целей
  `{targets, labels}` — нативный формат file_sd) с подсказками по
  special-лейблам, автодополнением и валидацией (`__scheme__`,
  `__metrics_path__`, `__scrape_interval__`, `__scrape_timeout__`,
  `__param_<name>`);
- **никогда не изменяет `prometheus.yml`**: если вновь созданный файл
  не покрывается ни одним глобом его джоба, PromUI показывает
  предупреждение и готовый к копированию фрагмент недостающего
  `file_sd_configs` (или целый блок `scrape_configs` для неизвестного
  джоба);
- создаёт бэкап перед каждым сохранением/удалением:
  `<name>.bkp_<YYYYMMDD_HHMMSS>_<9-значные ns>` — суффикс не попадает
  под глобы `*.yml`, поэтому Prometheus никогда не загружает бэкапы.
  Бэкапами управляют вручную; UI только уведомляет об их создании.

## Сборка

```sh
go build -o promui ./cmd/promui
go vet ./... && go test ./...
```

## Запуск

```sh
./promui \
  --files-sd-dir /var/lib/prometheus/files_sd \  # required, host dir
  --prometheus-url http://127.0.0.1:9090 \       # read-only API
  --listen 127.0.0.1:8080 \                      # UI address
  --htpasswd /etc/promui/htpasswd                # optional Basic Auth
```

Флаги:

| флаг | по умолчанию | назначение |
| --- | --- | --- |
| `--files-sd-dir` | — (обязательный) | каталог с файлами file_sd |
| `--prometheus-url` | `http://127.0.0.1:9090` | базовый URL Prometheus |
| `--listen` | `127.0.0.1:8080` | адрес прослушивания |
| `--htpasswd` | пустой (без auth) | файл htpasswd; перечитывается при каждом запросе; поддерживаются `$apr1$`, bcrypt, SHA-crypt |
| `--files-sd-mount` | `/files_sd` | путь, под которым Prometheus видит каталог (используется в предлагаемых фрагментах) |
| `--version` | `false` | вывести версию и выйти |

Флаги можно задать и переменными окружения — `PROMUI_FILES_SD_DIR`,
`PROMUI_PROMETHEUS_URL`, `PROMUI_LISTEN`, `PROMUI_HTPASSWD`,
`PROMUI_FILES_SD_MOUNT`; явно переданный флаг имеет приоритет над
окружением.

Файл htpasswd создаётся вне promui, например `htpasswd -B -c file admin`.

## Docker

Образ: `ghcr.io/supme/promui:latest` либо тег версии `ghcr.io/supme/promui:0.0.1`

### демо-стек

Готовый к запуску стек с Prometheus, Blackbox exporter и PromUI лежит
в `deploy/example`:

```sh
cd deploy/example
docker compose up -d --build
```

| сервис | URL |
| --- | --- |
| prometheus | http://localhost:9090 |
| blackbox-exporter | http://localhost:9115 |
| promui | http://localhost:8080 |

`deploy/example/prometheus.yml` задаёт scrape-джобы для node_exporter,
windows_exporter и blackbox-зондов (icmp / http / tcp / tls); все они
снабжаются целями из `file_sd` глобов
(`/etc/prometheus/files_sd/<job>_*.yml`). Примеры целей — плоские файлы
в `deploy/example/files_sd/` с именами `<job>_<name>.yml`, подпадающие
под эти глобы.

Оба контейнера bind-монтируют один и тот же каталог хоста
`deploy/example/files_sd/`: Prometheus читает его как
`/etc/prometheus/files_sd`, PromUI редактирует как `/data/files_sd`.
Поэтому файлы примеров видны уже с первого запуска, каждое изменение в
UI попадает прямо в этот каталог хоста и подхватывается Prometheus
через `refresh_interval` (здесь 15 с) — без перезапусков.

ICMP-зондам нужна capability `NET_RAW` (уже задана для
blackbox-exporter в `deploy/example/compose.yml`); на некоторых хостах
может понадобиться также
`sysctl net.ipv4.ping_group_range="0 2147483647"`.

## Страницы и API

- `GET /` — единственная страница: джобы с `file_sd_configs` и их файлы
  (по реальным глобам), таблица целей, форма создания файла с
  предупреждениями о покрытии, редактор групп (модалка) со справкой по
  special-лейблам.
- `GET /api/config` — разобранные джобы, сопоставление файл→джоб,
  «Другие».
- `GET /api/targets` — активные цели.
- `GET /api/files` — список файлов каталога (работает даже при
  недоступном Prometheus).
- `GET/PUT/DELETE /api/files/{name}`, `POST /api/files` — CRUD файлов с
  валидацией, атомарной записью и бэкапами.

Изменения вступают в силу через `refresh_interval` джоба (по умолчанию
5 мин) — поставьте меньше в `prometheus.yml`, если итерируете быстро.

## Заметки

- Редактирование перезаписывает файл целиком: нестандартные поля
  теряются, сохраняется только ведущий блок комментариев.
- Special-лейблы действуют в пределах группы; целям, которым нужны
  разные `metrics_path`/`scheme`, нужны отдельные группы.
- Используются только две намеренные сторонние зависимости:
  `go.yaml.in/yaml/v4` и `github.com/abbot/go-http-auth`.
