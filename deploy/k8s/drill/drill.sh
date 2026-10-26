#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
k=(kubectl --context kind-blood-supply)
[[ $("${k[@]}" get namespace bs-data -o jsonpath='{.metadata.labels.blood-supply\.io/env}') =~ ^(local|ci)$ ]] || { printf '%s\n' 'Учение разрешено только в демо-overlay local/ci.' >&2; exit 2; }
if "${k[@]}" -n bs-data get cluster pg-drill >/dev/null 2>&1; then
  printf '%s\n' 'pg-drill уже существует: сохраните прежний отчёт и удалите только учебный кластер вручную.' >&2
  exit 2
fi
instances=$("${k[@]}" -n bs-data get cluster pg -o jsonpath='{.spec.instances}')
[[ "$instances" =~ ^[0-9]+$ ]] && (( instances >= 2 )) || { printf '%s\n' 'До отказа primary нужны минимум два экземпляра PostgreSQL.' >&2; exit 2; }
"${k[@]}" -n bs-data wait --for=condition=Ready cluster/pg --timeout=300s
python3 tools/k8sdrill/application.py prepare
primary=$("${k[@]}" -n bs-data get cluster pg -o jsonpath='{.status.currentPrimary}')
[[ "$primary" =~ ^pg-[0-9]+$ ]] || { printf '%s\n' 'Не удалось определить primary pg.' >&2; exit 2; }
sql() { "${k[@]}" -n bs-data exec "$primary" -c postgres -- psql -X -A -t -v ON_ERROR_STOP=1 -U postgres -d postgres -c "$1"; }
marker="drill-$(date -u +%Y%m%d%H%M%S)"
sql "CREATE TABLE IF NOT EXISTS public.drill_marker (run text NOT NULL, phase text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(run,phase)); INSERT INTO public.drill_marker(run,phase) VALUES('$marker','before');" >/dev/null
"${k[@]}" -n bs-data apply -f - <<YAML
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: {name: $marker}
spec:
  cluster: {name: pg}
  method: barmanObjectStore
YAML
backup_ready=false
for attempt in $(seq 1 120); do
  phase=$("${k[@]}" -n bs-data get "backup/$marker" -o jsonpath='{.status.phase}')
  case "$phase" in
    completed) backup_ready=true; break ;;
    failed) "${k[@]}" -n bs-data describe "backup/$marker" >&2; exit 1 ;;
  esac
  sleep 5
done
[[ "$backup_ready" == true ]] || { "${k[@]}" -n bs-data describe "backup/$marker" >&2; exit 1; }
# PostgreSQL-формат с явным UTC: CNPG 1.27.0 некорректно преобразует RFC3339 Z.
target_time=$(sql "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS.US') || '+00';")
target_epoch=$(date -u +%s)
sql "INSERT INTO public.drill_marker(run,phase) VALUES('$marker','after');" >/dev/null
wal=$(sql 'SELECT pg_walfile_name(pg_current_wal_lsn());')
[[ "$wal" =~ ^[A-F0-9]{24}$ ]] || { printf '%s\n' 'Не определён сегмент WAL.' >&2; exit 1; }
sql 'SELECT pg_switch_wal();' >/dev/null
archived=false
for attempt in $(seq 1 90); do
  if [[ $(sql "SELECT coalesce(last_archived_wal >= '$wal',false) FROM pg_stat_archiver;") == t ]]; then archived=true; break; fi
  sleep 2
done
[[ "$archived" == true ]] || { printf '%s\n' 'Сегмент с маркерами не попал в архив: отказ primary отменён.' >&2; exit 1; }
failure_epoch=$(date -u +%s)
"${k[@]}" -n bs-data delete pod "$primary" --wait=false
temporary=$(mktemp)
trap 'rm -f "$temporary"' EXIT
pitr_time_pattern='^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}[.][0-9]{6}[+]00$'
[[ "$target_time" =~ $pitr_time_pattern ]] || { printf '%s\n' 'Неверная целевая точка PITR.' >&2; exit 1; }
printf 'Копия %s завершена, WAL %s сохранён, цель PITR: %s\n' "$marker" "$wal" "$target_time"
sed "s/__TARGET_TIME__/$target_time/" deploy/k8s/drill/restore.yaml.template > "$temporary"
"${k[@]}" apply --server-side -f "$temporary"
if ! "${k[@]}" -n bs-data wait --for=condition=Ready cluster/pg-drill --timeout=1800s; then
  "${k[@]}" -n bs-data describe cluster/pg-drill >&2 || true
  "${k[@]}" -n bs-data logs -l cnpg.io/cluster=pg-drill --all-containers --prefix --tail=200 >&2 || true
  exit 1
fi
restored=$("${k[@]}" -n bs-data get cluster pg-drill -o jsonpath='{.status.currentPrimary}')
[[ "$restored" =~ ^pg-drill-[0-9]+$ ]] || exit 1
counts=$("${k[@]}" -n bs-data exec "$restored" -c postgres -- psql -X -A -t -v ON_ERROR_STOP=1 -U postgres -d postgres -c "SELECT count(*) FILTER(WHERE phase='before'),count(*) FILTER(WHERE phase='after') FROM public.drill_marker WHERE run='$marker';")
[[ "$counts" == '1|0' ]] || { printf 'PITR не подтверждён: before|after=%s\n' "$counts" >&2; exit 1; }
"${k[@]}" -n bs-data wait --for=condition=Ready cluster/pg --timeout=600s
rto=$(( $(date -u +%s) - failure_epoch ))
window=$(( failure_epoch - target_epoch ))
python3 tools/k8sdrill/application.py verify
mkdir -p docs/perf
report="docs/perf/$marker.md"
cat > "$report" <<REPORT
# Учение восстановления $marker

Контекст: kind-blood-supply. Источник: pg. Восстановление: pg-drill.
Целевая точка: $target_time.

- RTO до проверки маркеров и готовности исходного кластера: $rto с.
- Интервал между выбранной целью PITR и отказом: $window с.
- Маркер до цели присутствует, маркер после цели отсутствует: $counts.
- Наличие WAL до удаления primary проверено по pg_stat_archiver.
- Это управляемое учение PITR; интервал до выбранной точки не является общей гарантией RPO.
- Прикладной smoke восстановленной копии: сохранённая заявка, расшифровка номера случая,
  новая запись только в pg-drill, 9 readiness-проб, полный OIDC-вход и страницы трёх ролей.
- Все девять подключений к БД временно переведены на pg-drill; исходные подключения возвращены.
- Подробный результат приложения: dist/drill-application.json.

Ресурсы pg-drill и исходный архив сохранены для проверки результата.
REPORT
printf 'Отчёт: %s\n' "$report"
(( rto <= 3600 && window <= 900 )) || { printf '%s\n' 'Демонстрационные пределы RTO/окна восстановления превышены.' >&2; exit 1; }
