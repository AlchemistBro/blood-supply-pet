-- Одна база и одна роль на сервис (ADR-0005): чужие таблицы недоступны физически.
-- Пароли равны именам ролей: это ДЕМО-значения для локального стенда, в k8s они приходят из Secret.

CREATE ROLE inventory LOGIN PASSWORD 'inventory';
CREATE DATABASE inventory OWNER inventory;
REVOKE CONNECT ON DATABASE inventory FROM PUBLIC;

CREATE ROLE demand LOGIN PASSWORD 'demand';
CREATE DATABASE demand OWNER demand;
REVOKE CONNECT ON DATABASE demand FROM PUBLIC;

CREATE ROLE planning LOGIN PASSWORD 'planning';
CREATE DATABASE planning OWNER planning;
REVOKE CONNECT ON DATABASE planning FROM PUBLIC;

CREATE ROLE matching LOGIN PASSWORD 'matching';
CREATE DATABASE matching OWNER matching;
REVOKE CONNECT ON DATABASE matching FROM PUBLIC;

CREATE ROLE transfer LOGIN PASSWORD 'transfer';
CREATE DATABASE transfer OWNER transfer;
REVOKE CONNECT ON DATABASE transfer FROM PUBLIC;

CREATE ROLE logistics LOGIN PASSWORD 'logistics';
CREATE DATABASE logistics OWNER logistics;
REVOKE CONNECT ON DATABASE logistics FROM PUBLIC;

CREATE ROLE notification LOGIN PASSWORD 'notification';
CREATE DATABASE notification OWNER notification;
REVOKE CONNECT ON DATABASE notification FROM PUBLIC;

CREATE ROLE audit LOGIN PASSWORD 'audit';
CREATE DATABASE audit OWNER audit;
REVOKE CONNECT ON DATABASE audit FROM PUBLIC;

CREATE ROLE keycloak LOGIN PASSWORD 'keycloak';
CREATE DATABASE keycloak OWNER keycloak;
REVOKE CONNECT ON DATABASE keycloak FROM PUBLIC;
