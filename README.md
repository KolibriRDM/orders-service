# orders-service

Самостоятельный Go-проект и Git-репозиторий сервиса заказов Food Delivery.
Go 1.27.1; модуль `github.com/KolibriRDM/orders-service`.
Путь модуля подготовлен для будущего репозитория; remote пока не настроен.

## Разработка

```sh
cp .env.example .env # только если .env ещё отсутствует
make build
make test
make race
make vet
make run
```

При разделении существующий локальный `.env` уже перенесён. `make run` загружает
его; сам бинарник читает переменные окружения. Настоящие пароли в Git не входят.
Сборка и unit-тесты не требуют соседних проектов, `go.work` или `replace`.

## PostgreSQL и совместный запуск

Этот сервис владеет `orders_db` и ролью `orders_app`. Локальный PostgreSQL слушает
`127.0.0.1:15432`; SQL-миграции принадлежат этому репозиторию: `migrations/`.
Небольшой `internal/platform` также локальный: общего Go-проекта платформы нет.

В соседнем `../infrastructure`:

```sh
make db-up
make migrate
make seed-catalog
```

После этого запусти `make run` в отдельном терминале каждого сервиса.
HTTP этого сервиса: `http://127.0.0.1:8080`; `/healthz` проверяет процесс,
`/readyz` — его собственную БД. Сервисы останавливаются и перезапускаются отдельно.

Интеграционные тесты с временной БД запускаются из `../infrastructure`:

```sh
make test-orders-postgres
```

Без инфраструктурного репозитория можно подготовить отдельную тестовую БД,
применить `migrations/` и передать `ORDERS_TEST_DATABASE_URL` и
`ORDERS_TEST_DB_PASSWORD`, затем выполнить `make test-integration`.
Имя тестовой БД должно начинаться с `orders_test_`; рабочая БД не подходит.

## Структура

```text
cmd/orders/main.go        сборка зависимостей и остановка процесса
internal/handler/        HTTP, DTO и middleware
internal/service/        бизнес-сценарии и интерфейсы зависимостей
internal/repository/     SQL и транзакции
internal/model/          бизнес-модели
internal/errors/         ошибки сервиса
internal/platform/       локальные конфигурация и pgxpool
migrations/              миграции только этого сервиса
Dockerfile               отдельный образ
```

Подробнее о слоях: [ARCHITECTURE.md](ARCHITECTURE.md).

## Контейнер

```sh
docker build -t food-delivery/orders:local .
```

Образ собирается из одного этого репозитория. Процесс работает без root.
Конфигурация передаётся окружением; `.env` исключён из build context.
При запуске в контейнере адрес БД должен указывать на доступный контейнеру
PostgreSQL, а не на `127.0.0.1`. Совместный Compose приложений — следующий этап.

## API заказов

- `POST /v1/orders` — создание с `X-Demo-User-ID` и `Idempotency-Key`.
- `GET /v1/orders/{id}` — чтение своего заказа с `X-Demo-User-ID`.

Новый заказ возвращает 201 и Location, совпадающий повтор — 200;
изменённый запрос с тем же ключом — 409. Чужой заказ скрывается за 404.
Позиции, заказ, ключ и outbox-событие сохраняются одной SQL-транзакцией.
Старые заказы хранят снимки названий и цен. Сохранённый повтор обрабатывается
до обращения к каталогу; недоступность catalog не мешает чтению старого заказа.

Сейчас `internal/catalogclient` обращается к отдельному catalog по HTTP:
таймаут 500 мс, ответ до 64 KiB, без автоматических повторов и redirects.
Внутренние пакеты catalog не импортируются. Переход на gRPC — следующий этап;
интерфейс `service.CatalogProvider` и бизнес-правила сохранятся.

Настройки: `ORDERS_HTTP_ADDR` (`:8080`), обязательные `ORDERS_DATABASE_URL` /
`ORDERS_DB_PASSWORD`, `CATALOG_BASE_URL` (`http://127.0.0.1:8082`), `LOG_LEVEL` (`info`).
`repository/memory` сохранён вместе с тестами, но приложение его не использует.
Kafka ещё не подключена: события остаются pending в outbox.
