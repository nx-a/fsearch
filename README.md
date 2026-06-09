# fsearch

Встраиваемый движок нечёткого поиска по плоским JSON-записям на базе
[bbolt](https://github.com/etcd-io/bbolt).

## Возможности

- **Несколько списков** — каждый идентифицируется `sysname`, изолированные
  индекс и хранилище.
- **Нечёткий поиск по триграммам** по явно заданным полям.
- **Межалфавитный поиск** через русско-английскую транслитерацию: `ivanov`
  находит `Иванов` и наоборот.
- **Минимальная длина запроса** — 5 символов (настраивается).
- **Хранение в bbolt** — всё в одном файле.
- **Кеш частых запросов** — LRU с учётом частоты кеширует запрос только после
  `CacheMinHits` обращений, затем отдаёт из памяти.
- **Битсет-списки вхождений** — очень частые триграммы (> `DenseThreshold`
  записей) хранятся компактным битсетом вместо разреженного `[]uint64`.
- **Параллельный Левенштейн** — если запрос даёт более 50 кандидатов, подсчёт
  расстояния распределяется по `GOMAXPROCS` воркерам.

## Структура

```
fsearch.go              публичный API движка (Open, CreateList, AddRecord, Search, ...)
value.go                преобразование значений + цели сравнения
httpapi/                REST-обёртка (HTTP-сервер)
internal/translit       нормализация + транслитерация RU<->EN
internal/trigram        извлечение триграмм
internal/postings       разреженные/битсет списки вхождений + сериализация
internal/bitset         компактный расширяемый битсет
internal/levenshtein    редакторское расстояние + параллельный подсчёт
internal/lru            LRU-кеш запросов с учётом частоты
internal/store          хранение в bbolt
cmd/server              CLI и HTTP-сервер
```

## Использование как библиотеки

```go
e, _ := fsearch.Open(fsearch.Options{Path: "data.db"})
defer e.Close()

e.CreateList("clients", []string{"name", "city"})
id, _ := e.AddRecord("clients", map[string]any{"name": "Иванов Иван", "city": "Москва"})

e.UpdateRecord("clients", id, map[string]any{"name": "Иванов И.", "city": "Москва"})
e.DeleteRecord("clients", id)

results, _ := e.Search("clients", "name", "ivanov", 10)
// results[0].Record["name"] == "Иванов Иван"
```

Поля `Options` (все необязательны, кроме `Path`): `MinQueryLen` (5),
`CacheCapacity` (1024), `CacheMinHits` (3), `MaxCandidates` (2000),
`MinScore` (0.5).

## CLI

```sh
go build -o fsearch ./cmd/server

./fsearch create -db data.db -list clients -fields name,city
echo '{"name":"Иванов Иван","city":"Москва"}' | ./fsearch add -db data.db -list clients
./fsearch import -db data.db -list clients -file records.json   # JSON-массив
./fsearch search -db data.db -list clients -field name -q ivanov -limit 10
./fsearch lists  -db data.db
./fsearch serve  -db data.db -addr :8080
```

## HTTP REST API

Запуск сервера:

```sh
./fsearch serve -db data.db -addr :8080
```

| Метод  | Путь                              | Описание                                  |
| ------ | --------------------------------- | ----------------------------------------- |
| GET    | `/lists`                          | список `sysname`                          |
| POST   | `/lists`                          | создать список `{sysname, search_fields}` |
| GET    | `/lists/{sysname}`                | метаданные списка                         |
| DELETE | `/lists/{sysname}`                | удалить список                            |
| POST   | `/lists/{sysname}/records`        | добавить запись (объект) или записи (массив) |
| GET    | `/lists/{sysname}/records/{id}`   | получить запись                           |
| PUT    | `/lists/{sysname}/records/{id}`   | обновить запись                           |
| DELETE | `/lists/{sysname}/records/{id}`   | удалить запись                            |
| GET    | `/lists/{sysname}/search`         | нечёткий поиск (`?field=&q=&limit=`)      |
| GET    | `/swagger-ui`                     | Swagger UI                                |
| GET    | `/openapi.yaml`                   | спецификация OpenAPI 3.0                  |

Базовый сегмент путей (`lists` в таблице выше) задаётся переменной
`CHANNEL_PREFIX`. Спецификация OpenAPI формируется налёту с учётом этого
префикса, поэтому Swagger всегда соответствует фактическим маршрутам и не
требует ручной правки.

После запуска сервера документация доступна в браузере по адресу
`http://localhost:8080/swagger-ui` (Swagger UI), а сама спецификация — по
`http://localhost:8080/openapi.yaml`.

Примеры:

```sh
# создать список
curl -X POST localhost:8080/lists \
  -d '{"sysname":"clients","search_fields":["name","city"]}'

# добавить записи (массив)
curl -X POST localhost:8080/lists/clients/records \
  -d '[{"name":"Иванов Иван","city":"Москва"},{"name":"Петров Пётр","city":"Казань"}]'

# обновить запись
curl -X PUT localhost:8080/lists/clients/records/1 \
  -d '{"name":"Иванов И.","city":"Москва"}'

# удалить запись / список
curl -X DELETE localhost:8080/lists/clients/records/1
curl -X DELETE localhost:8080/lists/clients

# поиск (латиница находит кириллицу)
curl 'localhost:8080/lists/clients/search?field=name&q=ivanov&limit=10'
```

Коды ответов: `404` — список/запись не найдены, `409` — список уже существует,
`400` — некорректный запрос (в т.ч. запрос короче 5 символов), `500` — прочие ошибки.

## Тесты

```sh
go test ./...
```
