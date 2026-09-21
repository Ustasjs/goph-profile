# GophProfile

Сервис аватарок: REST API для загрузки картинок, асинхронное создание миниатюр и веб-интерфейс. Учебный проект Яндекс.Практикума.

## Архитектура

```
клиент ──HTTP──> server ──┬── PostgreSQL   метаданные (таблица avatars)
                          ├── MinIO (S3)   оригиналы и миниатюры
                          └── RabbitMQ ──> worker ──> MinIO + PostgreSQL
```

- **server** (`cmd/server`) — REST API и веб-интерфейс. Принимает файл, кладёт оригинал в S3, метаданные в PostgreSQL и публикует событие в RabbitMQ. Файлы отдаёт проксированием из S3 — наружу торчит только HTTP API.
- **worker** (`cmd/worker`) — консюмер событий. Строит миниатюры 100x100 и 300x300 (всегда JPEG), удаляет объекты из S3 после удаления аватарки.
- **Надёжность** — ретраи с экспоненциальным backoff (1s/4s/16s) внутри консюмера, после исчерпания сообщение уходит в dead-letter очередь `avatar.dead`, а аватарка помечается `failed`. Обработка идемпотентна: повторная доставка события ничего не ломает.
- Удаление — мягкое: строка помечается `deleted_at`, объекты из S3 подчищает worker асинхронно.

## Быстрый старт

Всё в контейнерах:

```bash
docker compose --profile app up -d --build
```

Веб-интерфейс: <http://localhost:8080/web/upload>, галерея: `http://localhost:8080/web/gallery/{user_id}`.

Для локальной разработки — только инфраструктура, сервер и worker нативно:

```bash
docker compose up -d          # postgres, minio, rabbitmq
cp .env.example .env
make run                      # сервер
make run-worker               # worker (в другом терминале)
```

## API

Аутентификации нет: пользователя идентифицирует заголовок `X-User-ID` (обязателен для загрузки и удаления). Чтение — публичное.

| Метод и путь | Описание |
|---|---|
| `POST /api/v1/avatars` | Загрузка (multipart-поле `file` или `image`, до 10MB). 201 → `{id, user_id, url, status, created_at}`; 400 без заголовка/файла; 413 при превышении размера |
| `GET /api/v1/avatars/{id}` | Оригинал (бинарно, с Content-Type) |
| `GET /api/v1/avatars/{id}/thumbnails/{size}` | Миниатюра `100x100` или `300x300`; 404, пока обработка не завершена |
| `GET /api/v1/avatars/{id}/metadata` | Метаданные: размеры, миниатюры, статус обработки |
| `DELETE /api/v1/avatars/{id}` | Удаление; 403 — чужая аватарка; 204 — успех |
| `GET /api/v1/users/{uid}/avatar` | Последняя аватарка пользователя |
| `DELETE /api/v1/users/{uid}/avatar` | Удаление последней (только своей) |
| `GET /api/v1/users/{uid}/avatars` | Список аватарок пользователя |
| `GET /health` | Статусы компонентов: db, s3, broker; 503 при деградации |
| `GET /` , `GET /web/upload` | Форма загрузки |
| `GET /web/gallery/{uid}` | Галерея пользователя |

Статусы обработки в метаданных: `pending` → `processing` → `completed`, либо `failed` (например, файл не является картинкой), либо `deleted`.

## Конфигурация

Переменные окружения (флаги командной строки переопределяют, см. `-h`):

| Переменная | Назначение | Дефолт |
|---|---|---|
| `RUN_ADDRESS` | адрес HTTP-сервера | `:8080` |
| `DATABASE_DSN` | строка подключения PostgreSQL | — (обязательна) |
| `S3_ENDPOINT` | S3 endpoint (host:port) | — (обязательна) |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | ключи S3 | — (обязательны) |
| `S3_BUCKET` | бакет | `avatars` |
| `S3_USE_SSL` | https к S3 | `false` |
| `RABBITMQ_URL` | строка подключения AMQP | — (обязательна) |
| `WORKER_PREFETCH` | лимит неподтверждённых сообщений worker'а | `8` |
| `LOG_LEVEL` | уровень логов zap | `info` |

## Разработка

| Команда | Описание |
|---|---|
| `make run` / `make run-worker` | Запуск сервера / worker'а |
| `make test` | Unit-тесты (интеграционные скипаются без окружения) |
| `make test-integration` | Все тесты против docker-compose инфраструктуры |
| `make cover` | Покрытие по всем тестам |
| `make lint` | golangci-lint |
| `make build` | Бинарники в `bin/` |

Миграции применяет сервер при старте (embedded, golang-migrate). Worker миграции не запускает — в compose он стартует после того, как сервер станет healthy.

Интеграционные тесты делят очереди RabbitMQ с приложением: запущенный worker (`--profile app` или `make run-worker`) будет перехватывать тестовые события. Перед `make test-integration` остановите его: `docker compose --profile app stop server worker`.
