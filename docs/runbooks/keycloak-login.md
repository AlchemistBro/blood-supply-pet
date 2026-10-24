# Вход после обновления локального Keycloak

## Симптом

Пароль принимается, затем web показывает «Вход невозможен» из-за ФИО/email в токене.
После удаления персональных scope может появиться ошибка несовпадения участников id_token и access token.

## Причина

Существующий realm в базе Keycloak сохраняется при `task up`: повторный `--import-realm` его не обновляет.
Поэтому исправленный JSON в репозитории не означает, что настройки работающего клиента уже изменились.
В наблюдавшемся запуске web сохранял старые default scope profile/email и не имел отдельного mapper логина.
Кроме того, конфигурация проекта пропускала basic: в Keycloak 26.4 через него выдаётся sub access token.

## Исправление без удаления данных

В локальной административной консоли Keycloak выберите realm `blood-supply`, клиент `web`.
Сверяйтесь с [актуальным JSON](../../deploy/compose/keycloak/realm-blood-supply.json):

1. В назначенных client scopes у web оставьте basic, web-origins, acr, roles как Default.
2. Уберите назначения profile и email именно у web; не удаляйте общие scope realm.
3. Проверьте отдельный mapper preferred_username из JSON: свойство пользователя username,
   имя claim preferred_username, включение в access token. Не заменяйте его полным scope profile.
4. Начните новый вход через `https://localhost/login`, лучше в отдельном приватном окне браузера.
   Старую ссылку `/auth/callback?...code=...` повторно не открывайте: это одноразовый ответ входа.

Перезапуск контейнеров, повтор seed, смена пароля и удаление тома PostgreSQL для этой ошибки не нужны.
Не отключайте проверку персональных полей или совпадения sub в приложении.
Настройки можно изменить и через [официальный Admin REST API](https://www.keycloak.org/docs-api/latest/rest-api/index.html).

## Что проверено 24.10.2026

В уже работающем локальном Keycloak назначения web исправлены через Admin API без перезапуска.
Добавлен mapper preferred_username и basic, сняты profile/email. Пользователи и базы сохранены.
Проверка свежего потока authorization code через локальный HTTP-клиент завершилась на главной HTTPS-странице
с пользователем analyst1 (HTTP 200); страница /demand также вернула HTTP 200.
Это проверка работающего приложения HTTP-клиентом, не браузерный или нагрузочный тест.

JSON compose и Kubernetes исправлены. Регрессионный `TestKeycloakWebHasIdentityWithoutPersonalScopes`
сначала упал из-за отсутствия basic в обоих файлах, после исправления прошёл.
Он также проверяет отсутствие персональных scope и наличие mapper логина.
Базы текущего compose находятся в локальном контейнере postgres, данные — в blood-supply_pgdata.
