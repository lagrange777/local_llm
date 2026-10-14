# Приватный AI-сервис — задание 30

Текущий local_llm дополнен приватным режимом: HTTP API, веб-чат, вход, ограниченная очередь и лимиты генерации. Модель выполняется в Ollama на вашем сервере. Облачные модели не используются. RAG и источники доступны как раньше.

**Статус проверки:** по согласованному с пользователем объёму подготовлен Docker-пакет и приложение проверено локально с реальной Qwen. Развёртывание на VPS/домашнем сервере, регистрация Tailscale и запрос с другого устройства пока не выполнены: данные сервера и аккаунта не предоставлены. Конфигурация подготовлена для этого этапа; локальный прогон не заменяет сетевую проверку.

## Архитектура

Клиент → Tailscale HTTPS :443 → Nginx → Go-приложение → Ollama.

Все компоненты, включая необязательный Tailscale sidecar, упакованы в Docker. Tailscale работает в userspace, без privileged, NET_ADMIN и доступа к Docker socket. Он разделяет network namespace gateway и проксирует на 127.0.0.1:8080. На хосте опубликован только 127.0.0.1:8081; у app и Ollama нет опубликованных портов и внешнего маршрута из internal-сети runtime. Интернет нужен для подготовки весов и Tailscale control plane. Генерация остаётся внутри runtime.

Образ Tailscale зафиксирован digest; локально проверен запуск его CLI, версия 1.104.1 и наличие containerboot/wget. Реальный Serve/HTTPS без аккаунта не проверен. Настройки следуют [официальной документации Docker](https://tailscale.com/docs/features/containers/docker/docker-params) и [примеру Serve](https://tailscale.com/blog/docker-tailscale-guide). Funnel не включён.

## Локальная проверка

Из корня репозитория:

```sh
python3 scripts/setup-private.py --origin http://127.0.0.1:8081
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml up -d --build --wait
```

Подготовка генерирует случайный 256-битный ключ в .secrets/service_token. Его значение не печатается. Существующий ключ не заменяется; повторное создание .private.env требует явного редактирования существующего файла. .secrets и .private.env исключены из Git и Docker build context.

Откройте http://127.0.0.1:8081. Ключ для ввода можно прочитать локально из .secrets/service_token; не публикуйте его. Browser login выдаёт случайную HttpOnly/SameSite=Strict cookie на 12 часов. Logout отзывает сессию; рестарт app отзывает все браузерные сессии. API-ключ остаётся действительным до его замены и рестарта app. История чата сохраняется только в браузере и не очищается при выходе.

Для loopback-теста setup устанавливает Secure=false. В production требуется HTTPS и Secure=true. HTTP или insecure cookie для внешнего hostname не позволяют запустить приватный сервис. Секрет монтируется файлом только в app и не входит в environment. Docker Compose сохраняет права исходного файла: он читаем non-root контейнером (0644), а каталог на хосте имеет 0700.

Основной профиль приватного развёртывания — instruct-4b-q4. Исходная 8B и другие профили доступны, но подчиняются общим лимитам. Исторический отчёт задания 29 описывает другие условия эксперимента; он не является измерением новых ограничений сервиса.

## VPS или домашний Linux-сервер

Ориентир для первого CPU-развёртывания: 16 ГБ RAM, 4–8 vCPU, 40–60 ГБ SSD. Скорость на выбранном сервере нужно измерить отдельно. Linux/NVIDIA вариант compose.gpu.yaml сохранён, на текущем Mac он не испытывался.

1. Установите Docker Engine и Compose на выбранный сервер, клонируйте репозиторий.
2. В Tailscale включите MagicDNS и HTTPS. Подготовьте policy по deploy/tailnet-policy.example.json: замените owner@example.com своим аккаунтом, ограничьте tag:local-llm портом 443. Проверьте policy в консоли. Этот пример рассчитан на отдельный tailnet; при объединении с существующей policy широкие разрешения на тот же узел должны быть убраны, иначе приватность не обеспечена.
3. Создайте auth key для tag:local-llm. Сохраните его на сервере в .secrets/tailscale_authkey без передачи в Git, команды или историю чата. Это отдельный ключ, не ключ API приложения. Каталог .secrets должен иметь 0700.
4. Выберите свободное имя узла, например local-llm, и его ожидаемое полное HTTPS-имя в tailnet.
5. Подготовьте конфигурацию и запустите контейнеры:

```sh
python3 scripts/setup-private.py --origin https://local-llm.YOUR-TAILNET.ts.net
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml -f compose.tailnet.yaml up -d --build --wait
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml -f compose.tailnet.yaml exec tailscale tailscale status
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml -f compose.tailnet.yaml exec tailscale tailscale serve status
```

Если имя узла оказалось занято и Tailscale изменил его, обновите PRIVATE_PUBLIC_ORIGIN в .private.env до фактического HTTPS origin и пересоздайте app. Важно, чтобы Origin браузера совпадал точно. TS_CERT_DOMAIN в Serve config подставляется containerboot. TS_AUTH_ONCE и постоянный том tailnet-state сохраняют регистрацию после рестарта.

Порты 8080, 8081 и 11434 не нужно открывать в публичном firewall. С SSH и сетевой политикой сервера нужно учитывать существующий доступ администратора; конфигурация не меняет их автоматически. Tailscale auth key создаётся и policy применяется владельцем аккаунта.

## API

POST /api/chat сохраняет существующий NDJSON формат:

```json
{"messages":[{"role":"user","content":"Сколько будет 2 + 2? Ответь только числом."}],"think":false,"rag":false}
```

Передайте Authorization: Bearer <ключ из .secrets/service_token>. Ключ нужен также для чтения статуса, индекса и отчётов. Браузер использует cookie после входа. /healthz возвращает только liveness; готовность выбранной модели проверяется авторизованным GET /api/status.

GET /api/service/status показывает лимиты, активную генерацию, длину очереди и счётчики завершений/ошибок/отмен. Секретов и текстов диалога там нет. Сервер не доверяет X-Forwarded-For и Tailscale identity headers для авторизации или rate limit.

## Ограничения

| Ограничение | По умолчанию |
|---|---:|
| Запросы владельца | 6 за скользящие 60 секунд |
| Одновременная генерация | 1 |
| Ожидающие запросы | 2 |
| Контекст | Не более 4096 токенов |
| Выход | Не более 384 токенов |
| HTTP body | 64 КиБ |
| Тайм-аут, включая очередь | 5 минут |
| Попытки входа | 10 за минуту |
| Браузерные сессии | 64, срок 12 часов |

Это однопользовательский сервис: один ключ = одна учётная запись владельца. Cookie и Bearer используют одну квоту, новые сессии её не сбрасывают. Для нескольких независимых пользователей нужны отдельные ключи/учётные записи и отдельные квоты. Ограничитель хранится в памяти одного app; после рестарта окно сбрасывается. Горизонтальное масштабирование не заявляется.

Настройки администратора: PRIVATE_RPM, PRIVATE_QUEUE, PRIVATE_MAX_CONTEXT, PRIVATE_MAX_OUTPUT, PRIVATE_PROFILE, PRIVATE_MODEL_MEMORY в .private.env. Пользователь API не может передавать произвольные модели/options и обойти эти лимиты выбором baseline. Очередь ограничена, строгий FIFO не гарантируется.

429 содержит code=rate_limit или queue_full и Retry-After. Время в очереди входит в тайм-аут. Отмена освобождает очередь/генерацию и отменяет HTTP-запрос к Ollama. Превышение body возвращает 413; плохой JSON/options — 400. Переполнение оценочного бюджета контекста отклоняется, а не скрывается обрезкой plain-истории.

Бюджет входа оценивается по UTF-8 bytes/3 с резервом выхода; это эвристика, не точный токенизатор. Дополнительно фактический prompt_eval_count + резерв сравнивается с num_ctx. В приватном plain-режиме текст буферизуется до этой проверки: start приходит сразу, затем проверенные delta/done. При близком к пределу контексте текст не публикуется. RAG сохраняет существующее ограничение истории по полным парам; history_trimmed сообщается в start. Ответ с done_reason=length считается ошибкой, не завершённым ответом.

Логи содержат фиксированную метку маршрута, HTTP-статус, длительность и число выходных токенов. Нет query string, ключей, cookie, IP или текстов вопросов/ответов. Метрики сбрасываются при рестарте. Лимиты памяти/PID заданы в Compose; на Mac доступная память VM может быть ниже PRIVATE_MODEL_MEMORY.

## Проверки

```sh
# Запускайте с другого разрешённого устройства для проверки сети:
python3 scripts/verify-private.py \
  --url https://local-llm.YOUR-TAILNET.ts.net \
  --token-file /path/to/service_token \
  --phase all --output artifacts/private30-remote

# Для локального Docker-прогона:
python3 scripts/verify-private.py --token-file .secrets/service_token --phase all

# Дополнительный тест настоящего тайм-аута:
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml -f compose.private-timeout.yaml up -d --wait
python3 scripts/verify-private.py --token-file .secrets/service_token --phase timeout
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml up -d --wait

# Генерация после рестарта app:
docker compose --env-file .private.env -f compose.yaml -f compose.private.yaml restart app
python3 scripts/verify-private.py --token-file .secrets/service_token --phase smoke
```

20 последовательных запросов проверяют завершение потока и правильность простого ответа. Rate-limited повторы не скрыты, они сохранены в JSON. Тест ждёт настоящую минутную квоту, а не снижает её искусственно. Пять одновременных запросов проверяют 1 active + 2 queued + 2 отказа. Быстрый rate-тест использует вопросы без найденного RAG-контекста, чтобы отдельно проверить HTTP admission без долгой генерации.

Проверяются 401, чужой Origin, body/context limits, произвольные options/model, ограничения всех профилей, Retry-After, освобождение слотов после отмены и timeout. Отдельный 1s override предназначен только для проверки, production возвращается к 5m. Все ответы, ошибки и наблюдения сохраняются атомарно.

С другого устройства также проверьте: HTTPS-чат, API, отказ без членства в tailnet/без grant, отсутствие доступа к 11434/8080, перезапуск сервера и сохранение весов. Проверка Bearer 401 не заменяет проверку VPN ACL с неразрешённого устройства. У скрипта scope указывает loopback или network endpoint, но принадлежность устройства и policy нужно проверять отдельно.

Результаты локального прогона: [REPORT.md](artifacts/private30/REPORT.md). Для полного результата исходного задания остаётся реальное развёртывание и проверка с второго устройства.
