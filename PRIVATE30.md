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

Откройте http://127.0.0.1:8081. Для веб-чата нужен отдельный пароль: первоначальный сгенерированный пароль записан в .secrets/web_password.initial. API-ключ из .secrets/service_token используется только программами. Browser login выдаёт случайную HttpOnly/SameSite=Strict cookie на 12 часов. Logout отзывает сессию; рестарт app отзывает все браузерные сессии. API-ключ остаётся действительным до его замены и рестарта app. Docker получает только .secrets/web_password_hash, а файл с первоначальным паролем остаётся на Mac и не монтируется в контейнер. История чата сохраняется только в браузере и не очищается при выходе.

Для loopback-теста setup устанавливает Secure=false. В production требуется HTTPS и Secure=true. Без отдельного LAN opt-in HTTP или insecure cookie для внешнего hostname не позволяют запустить приватный сервис. API-ключ и хеш пароля монтируются отдельными файлами только в app и не входит в environment. Docker Compose сохраняет права исходного файла: он читаем non-root контейнером (0644), а каталог на хосте имеет 0700.

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

Это однопользовательский сервис с одной учётной записью владельца: пароль для веб-чата и отдельный API-ключ для программ. Cookie и Bearer используют одну квоту, новые сессии её не сбрасывают. Для нескольких независимых пользователей нужны отдельные ключи/учётные записи и отдельные квоты. Ограничитель хранится в памяти одного app; после рестарта окно сбрасывается. Горизонтальное масштабирование не заявляется.

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

## Домашняя сеть без HTTPS

Этот режим включается отдельно по явному выбору владельца. Ключ доступа, сессия и переписка передаются без шифрования; используйте доверенную домашнюю сеть.

Приватный режим, Bearer/HttpOnly/SameSite-авторизация и лимиты сохраняются. SERVICE_ALLOW_LAN_HTTP по умолчанию выключен; исключение разрешено только для буквального IPv4 из RFC1918 (10/8, 172.16/12, 192.168/16), с Secure=false. Публичные IP, wildcard-адреса, произвольные DNS-имена и HTTP без opt-in отклоняются. HTTPS-конфигурация Tailscale остаётся вариантом по умолчанию для внешнего доступа.

В корне проекта, после подготовки .private.env и ключа:

```sh
# Подставьте текущий домашний IPv4 сервера:
python3 scripts/setup-lan.py --ip 192.168.1.20
docker compose --env-file .private.env --env-file .lan.env \
  -f compose.yaml -f compose.private.yaml -f compose.lan.yaml up -d --build --wait

python3 scripts/verify-private.py --url http://192.168.1.20:8081 \
  --allow-lan-http --token-file .secrets/service_token --phase limits \
  --output artifacts/lan-check
```

Откройте http://IP-СЕРВЕРА:8081 на устройстве из той же домашней сети и введите веб-пароль из .secrets/web_password.initial (или пароль, установленный владельцем). Сертификаты и Tailscale в этом режиме не нужны. Loopback http://127.0.0.1:8081 также сохранён. Origin допускается только для заданного LAN origin и обращений с совпадающим loopback Host; другие сайты не принимаются.

compose.lan.yaml заменяет список публикаций gateway: одна привязка к loopback и одна к выбранному домашнему IP, без 0.0.0.0. У app/Ollama по-прежнему нет опубликованных портов. .lan.env содержит адрес текущего Mac и исключён из Git и build context. Ключ не заменяется. Скрипт проверки не отправляет ключ по произвольному HTTP: для RFC1918 требуется отдельный --allow-lan-http.

Mac должен быть включён, Docker запущен, устройства должны находиться в одной обычной Wi-Fi сети. Гостевая сеть/client isolation могут запрещать связь. Если IP изменился, повторите setup-lan.py с новым адресом и пересоздайте сервисы. При необходимости закрепите IP в DHCP-настройках своего роутера.

Проверка на самом сервере по его LAN IP подтверждает публикацию и HTTP API, но не заменяет проверку с другого физического устройства. Маршрутизатор, port forwarding, firewall, VPN и системное доверие сертификатам автоматически не изменяются.

## Пароль веб-чата и API-ключ

POST /api/auth/login принимает только {"password":"..."} и выдаёт браузерную сессию. Поле token больше не принимается; API-ключ не позволяет войти через форму пароля. Пароль не принимается как Bearer-токен API.

Хеш пароля хранится в .secrets/web_password_hash: PBKDF2-HMAC-SHA256, 600000 итераций, случайная соль 16 байт, результат 32 байта. Проверка использует постоянное по времени сравнение. Формат и границы параметров проверяются до запуска; отсутствующий или некорректный хеш останавливает приватный сервис. Используется стандартная crypto/pbkdf2 без дополнительных Go-зависимостей. Значение work factor соответствует [рекомендациям OWASP для PBKDF2](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html#pbkdf2).

Для существующего развёртывания подготовьте пароль один раз:

```sh
python3 scripts/set-web-password.py --generate --if-missing
```

Чтобы установить свой пароль, выполните на сервере команду ниже. Ввод скрыт, пароль не передаётся аргументом или через environment. Допускается 12–256 байт UTF-8; API-ключ как пароль запрещён. Команда сохраняет только новый хеш и удаляет устаревшую записку с первоначальным паролем.

```sh
python3 scripts/set-web-password.py
docker compose --env-file .private.env --env-file .lan.env \
  -f compose.yaml -f compose.private.yaml -f compose.lan.yaml \
  up -d --force-recreate app
```

Повторное --generate --if-missing сохраняет существующий пароль. Явное --generate создаёт новый случайный пароль и приватную локальную записку .secrets/web_password.initial (0600). Каталог .secrets имеет 0700; все его файлы исключены из Git/build context. Пароль, хеш и API-ключ не попадают в логи или status API. Смена пароля не меняет API-ключ; пересоздание app отзывает старые браузерные сессии.

HTTP-режим домашней сети сохранён по выбору владельца: замена ключа на пароль не добавляет шифрование транспорта.
