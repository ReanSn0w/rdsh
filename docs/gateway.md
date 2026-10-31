# Шлюз rdsh

## Проверенный протокол DSH

Для первого выпуска выбрана опубликованная версия `@deepseek-ai/dsh@0.2.1-rc.5`.
Контейнерный прототип runtime подтвердил:

- `GET /?token=…` с публичным `Host` возвращает `303`, `Location: ./` и cookie `dsh-auth-*`.
- HTTP API отклоняет запрос без cookie; WebSocket `/api/remote.mux` с cookie и корректным `Origin` принимает upgrade.
- После перезапуска DSH стартовый токен меняется, старый токен получает `401`.
- При сохранении домашнего каталога уже выданная браузерная cookie продолжает работать.

Полные условия и воспроизводимый прототип принадлежат [интеграции runtime](dsh-integration.md).
Смена версии DSH требует повторной проверки этого протокола до её включения в поддерживаемый пример.
Это адаптер под конкретный upstream, а не универсальная система SSO.

## Контракт контейнеров

`gateway` обращается к фиксированному `DSH_UPSTREAM` (обычно `http://dsh:3080`).
Адрес upstream никогда не берётся из браузерного запроса или handoff-файла.
`dsh` атомарно заменяет `/run/rdsh/connection.json` с двумя строковыми полями:
`token` и `generation`. Шлюз монтирует этот служебный том только для чтения.
Отсутствующий, повреждённый или меняющийся handoff закрывает доступ с временной ошибкой,
а не отключает проверку авторизации. Возможен максимум один дополнительный обмен при смене поколения.
HOME DSH, его файл credentials и модельные ключи шлюзу не монтируются.

Публичный URL задаётся явно, использует HTTPS и корневой путь. Prefix deployments в первом выпуске не поддерживаются.
Служебные маршруты шлюза занимают `/_rdsh/`: `login`, `logout`, `style.css`, `health`.
Все остальные маршруты принадлежат DSH и требуют сессию шлюза, в том числе статические файлы, API и WebSocket.
Прямой браузерный обмен query-токена DSH запрещён.

Реализация: один Go-процесс, `net/http/httputil.ReverseProxy` для потоков и WebSocket,
`golang.org/x/crypto/bcrypt` для пароля, `github.com/gorilla/securecookie` для подписанных cookie,
`golang.org/x/crypto/acme/autocert` для ACME и продления сертификатов.
Сессии дополнительно хранятся в ограниченной серверной таблице с отзывом и временем жизни.
Собственная криптография и отдельный сервис базы данных не нужны.

| Настройка | Назначение / значение по умолчанию |
| --- | --- |
| `PUBLIC_URL` | Обязательный внешний HTTPS origin, например `https://dsh.example.com` |
| `TLS_MODE` | `external` (HTTP за существующим TLS proxy) или `auto` (ACME) |
| `HTTP_ADDR` | `:8080` |
| `HTTPS_ADDR` | `:8443`, только в `auto` |
| `AUTH_USERNAME` | `token` |
| `AUTH_PASSWORD_HASH_FILE` | `/run/secrets/password_hash` |
| `SESSION_SECRET_FILE` | `/run/secrets/session_secret` |
| `SESSION_TTL` | `12h`; допустимо `5m`–`720h` |
| `DSH_UPSTREAM` | `http://dsh:3080` |
| `DSH_HANDOFF_FILE` | `/run/rdsh/connection.json` |
| `STATE_DIR` | `/var/lib/rdsh`, постоянный ACME cache в подкаталоге `acme` |
| `TRUSTED_PROXIES` | Список CIDR через запятую; пустой означает не доверять forwarded IP |
| `ACME_EMAIL` | Контакт для ACME, необязательный |
| `ACME_DIRECTORY_URL` | Необязательная альтернативная ACME directory, например staging |

Образ принимает build args `RDSH_UID`/`RDSH_GID`, по умолчанию `1000`/`1000`.
Каталоги состояния и handoff создаются с тем же владельцем, что и в образе DSH,
чтобы начальное заполнение named volume не зависело от порядка создания контейнеров.
