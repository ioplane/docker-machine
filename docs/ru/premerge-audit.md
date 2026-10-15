# Проверка перед merge

Дата: 2026-10-15. Исходный driver72a639c0156d7fa1844a43eea79244d35f86f706,
SDK5af0cc292b31f2a02f190297ef6e2a0c8540de6d. Исследованы 103 non-test
functions, 51 reachable plugin functions, 0 собственных production goroutines.
Go1.16–1.27 release notes и Go skills сопоставлены с RPC/JSON/HTTP paths.

## Подтверждённые исправления

SDK: каждый non-2xx возвращает ошибку; исходный response body закрывается ровно
один раз; malformed filtered JSON возвращает ошибку без panic. Изоляция TLS/proxy
сохраняется. Driver: missing VM metadata отклоняется до dereference; полученный
VM UUID сохраняется до проверки task; частичный NIC profile не достигает Create.
Это не durable journal: RPC/process failure всё ещё требует reconcile.

## Изолированная интеграция

Отдельный tests/integration module закрепляет Testcontainers v0.44.0 и не добавляет
Docker/Moby зависимости в production go.mod. Один Prism TLS fixture на suite:
queues для UUID/VLAN/OVERLAY, HTTP429/500, invalid JSON, missing metadata,
missing additional subnet, один Create POST без автоматического повторения.
Fixtures не выполняют VM provisioning. Rootless socket, CleanupContainer,
отдельные readonly/tidy/vet/lint/fmt/test-aware vuln gates обязательны.

## Оставшиеся P1 gates

| Beads | Требуемая работа |
|:---|:---|
| um-docs-nrsu.4.8.1 | Operation journal/reconcile, unknown outcome, запрет удаления только из-за отсутствия IP, идемпотентный Remove |
| um-docs-nrsu.4.8.2 | Timeout input validation, общий HTTP/poll deadline, terminal power errors, cancel-aware waitTask с synctest |
| um-docs-nrsu.4.8.3 | Input order NIC, unique resolution, explicit management IP identity, nil-safe endpoints |
| um-docs-nrsu.4.8.4 | Top-level cloud-init users/root merge; CPU/RAM/disk validation независимо от queues |
| um-docs-nrsu.4.8.5 | Pagination/request budget, one GPU inventory fetch, измерение transport ownership/idle cleanup |

SDK error bodies пока не имеют общего bounded/sanitized contract. Это отдельный
security follow-up, не исправленная возможность. Нет основания включать новые
worker pools, глобальный cache, blind retry Create или универсальные sysctl.

## Go и границы совместимости

Compiler1.27.2 фиксируется OCI digest и GOTOOLCHAIN=local. Final SDK language floor повышен до1.27 после standalone dependency audit;
потребителям форка требуется Go1.27+. Main drivergo1.27 определяет stdlib compatibility defaults. Shared Go
plugin ABI неприменима к отдельному net/rpc процессу. Сохраняются API1/Config3,
legacy JSON и nullable queues. Go1.27 JSON engine не требует перехода на v2 imports.
Removed legacy timer/TLS GODEBUG switches не являются rollback.

Whole-SDK lint/vuln governance, explicit secret-diff/input/deadcode baseline,
release SPDX/CycloneDX/provenance остаются отдельными quality задачами.
Green CI не заменяет vendor support, Rancher canary, HA/storage/CNI и measured SLO.
