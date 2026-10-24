# Локальный замер планирования, 24.10.2026

Вход: 100 клиник, 100000 единиц; сценарий `BenchmarkBuildPlanNFR002`.
Windows amd64, Go 1.27.2, Intel Core Ultra 7 155H, `CGO_ENABLED=0`, один CPU для бенчмарка.
PostgreSQL, Docker, Kafka и HTTP в расчёте не участвуют.

```text
go test ./services/planning/internal/domain -run '^$' -bench '^BenchmarkBuildPlanNFR002$' -benchtime=1x -count=3 -cpu=1 -benchmem

48213400 ns/op    37647288 B/op    24816 allocs/op
53090000 ns/op    37647288 B/op    24816 allocs/op
50474800 ns/op    37647288 B/op    24816 allocs/op
```

Повтор с CPU/alloc-профилированием, `-benchtime=3s`: 52 операции, 58807127 нс/операцию
(58,8 мс), 37646985 байт/операцию, 24812 аллокаций/операцию. Результат: 13 предложений,
120 записей дефицита, коэффициент покрытия 0,2317. Все замеры меньше порога 30 секунд.

Основные потребители CPU по профилю: подготовка единиц и FEFO-сортировка, операции с map, сборка мусора.
`state.addUnits` занимает 51,1% CPU с учётом вложенных вызовов и 77,7% выделенной памяти;
`state.candidates` — 5,8% выделенной памяти. Срочная оптимизация алгоритма для этого набора не требуется.

Локальные артефакты: `dist/planning-benchmark.log`, `dist/planning-profile.log`,
`dist/planning.cpu.pprof`, `dist/planning.mem.pprof`, `dist/planning.test.exe`.
Они не коммитятся. Просмотр профилей: `go tool pprof -top dist/planning.cpu.pprof`
и `go tool pprof -top -alloc_space dist/planning.mem.pprof`.

Замер подтверждает время чистого алгоритма на указанном наборе и машине. Он не измеряет сохранение плана,
задержку проекций, HTTP p95, работу под нагрузкой или производительность развёрнутого стенда.
