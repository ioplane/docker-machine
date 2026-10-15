# Nutanix node driver: план модернизации v3

Дата: 2026-10-15. Источник задач — локальный Beads `um-docs-nrsu` в um-docs.
[Полный план, инфраструктурные эпики и критерии](https://um-git.infra4.dev/p.lavrukhin/um-docs/-/blob/sprint/nutanix-premerge-2026-10-15/docs/plans/nutanix-rke2-modernization/README.md).

## Порядок

Baseline и действующие quality gates → исправления SDK/node driver → HTTP/auth/input/deadline → operation identity/reconcile → структура → измеренная оптимизация → Rancher wiring → release → согласованный canary → scale/replace/upgrade.

Критичность и порядок независимы: P1/C1 recovery не выполняется до проверки
входа/deadline и quality gates. Статусы проверять в Beads; код в PR не означает
merge или внедрение. Каждая semantic wave начинается с RED regression и
оставляет проверенный совместимый consumer contract.

## Состав

| Beads | Работа | Этап | P/C |
|:---|:---|:---|:---|
| `um-docs-nrsu.6.10.5` | Pre-merge аудит Go 1.16–1.27, API/RE и Testcontainers | closed audit | P1/C2 |
| `um-docs-nrsu.6.10.12` | SDK/driver: baseline пакетов, входных данных и производительности | W0 | P1/C3 |
| `um-docs-nrsu.6.10.10` | Fork governance: Keep a Changelog и Conventional Commits | W1 | P1/C2 |
| `um-docs-nrsu.6.10.6` | Go quality: полный lint, formatter, input inventory и fuzz gates | W2 | P1/C2 |
| `um-docs-nrsu.6.10.1` | Node driver: форк ioplane и обновление Go/компонентов | W1 | P1/C3 |
| `um-docs-nrsu.6.10.2` | Node driver: golangci-lint v2 строгий профиль и CI | W1 | P1/C2 |
| `um-docs-nrsu.6.10.3` | Node driver: optional vNIC queues и контрактные тесты | W1 | P2/C3 |
| `um-docs-nrsu.6.10.4` | Prism SDK: изоляция HTTP transport и TLS между клиентами | W1 | P1/C2 |
| `um-docs-nrsu.6.10.7` | SDK Go 1.27 и устранение standalone dependency findings | W1 | P1/C2 |
| `um-docs-nrsu.4.8.6` | Ограничение HTTP body и безопасная диагностика SDK | W2 | P1/C2 |
| `um-docs-nrsu.4.8.7` | SDK: session-auth, replay upload и конкурентное создание клиентов | W2 | P1/C2 |
| `um-docs-nrsu.4.8.2` | Единый deadline и автомат состояний задач Prism | W2 | P1/C2 |
| `um-docs-nrsu.4.8.3` | Порядок NIC и выбор management IP по идентичности | W2 | P1/C2 |
| `um-docs-nrsu.4.8.4` | Валидация cloud-init и числовых hardware параметров | W2 | P1/C2 |
| `um-docs-nrsu.4.8.8` | SDK v4: terminal task states и отменяемый Operation.Wait | W2 | P1/C2 |
| `um-docs-nrsu.4.8.1` | Сохранение operation identity и безопасное восстановление Create/Delete | W3 | P1/C1 |
| `um-docs-nrsu.6.10.8` | SDK: структура пакетов, функций и совместимость публичного API | W4 | P2/C3 |
| `um-docs-nrsu.4.8.5` | Пагинация и HTTP budget inventory/GPU | W5 | P2/C3 |
| `um-docs-nrsu.6.10.9` | SDK: измерения производительности и повторяемости операций | W5 | P2/C3 |
| `um-docs-nrsu.6.10.11` | Release: SBOM, provenance и неизменяемый consumer pin | W7 | P2/C3 |

## Обязательные контракты

Сохранить package/import paths, exported signatures, nil/omitted/zero JSON,
RPC/config/state совместимость. Разделить длинные файлы по обязанностям внутри
существующих packages; не создавать новый framework. Реальные headers/API
semantics подтверждать для каждого endpoint/version. После unknown outcome
сначала reconcile; не выполнять blind retry Create или Delete из-за отсутствия IP.
Auth/body replay, concurrent cache и v4 terminal states проверять адресно.

Go tools, exact images/dependencies, readonly/tidy/verify, race/shuffle/atomic,
vet и whole graph govulncheck выполнять в Podman. Full SDK strict lint/fuzz
остаются отдельной задачей W2 после адресных исправлений; compiler/module inventory W0 включает nested nusights. Testcontainers проверяет HTTP boundary,
не полную семантику Prism. Performance измерять по одинаковым fixtures и input:
request/TLS/auth counts, allocations, bytes, memory/FD/goroutines и latency.

Keep a Changelog и Conventional Commits проверяются target governance.
Published history сохраняется; новые releases используют dated SemVer и
immutable artifacts с SBOM/provenance. Merge, Rancher registration, изменения
VM и нагрузка имеют отдельные acceptance/authorization gates.

## Контракт очередей

Optional `nutanix-vm-nic-queues` отсутствует по умолчанию. Пустой ввод сохраняет
прежний payload без num_queues. Явное значение — положительное decimal целое
не больше sockets × cores; переполнение отклоняется до Create. Применение ко
всем NIC не доказывает guest channels/IRQ или throughput. Schema/provider/module
mapping, cold boot/join и PC/guest readback относятся к последующим этапам.

## W0: воспроизводимые измерения

`make benchmark-check` проверяет assertions стенда с race detector;
`make benchmark` сохраняет пять выборок при GOMAXPROCS=4, 100 ms на
benchmark. Время не является CI threshold. Все команды исполняются через
существующий pinned Podman image. Производительность production из этих
fixtures не выводится. Compiler/module/API inventory собирается средствами
`go list -export -compiled -deps -test -json`, `go list -m -json all` и
`go doc -all` отдельно для каждого Go module. Числа и границы измерения
публикуются в каноническом плане um-docs; полные исходные результаты локальны.

[Датированный W0 baseline: compiler/API inputs, fixtures и распределения](https://um-git.infra4.dev/p.lavrukhin/um-docs/-/blob/sprint/nutanix-premerge-2026-10-15/docs/plans/nutanix-rke2-modernization/baseline-2026-10-15.md).
Root `./...` не охватывает nested modules. В SDK nusights имеет отдельный
module-level vulnerability gate; runtime pin driver в W0 не меняется.


## Приоритет разработки v3

По указанию владельца сначала исправлять SDK и node driver. Полный stylistic
lint/formatter SDK не блокирует адресные исправления `um-docs-nrsu.4.8.4` и
`um-docs-nrsu.4.8.6`; их prerequisite — закрытый W0 и действующие обязательные
checks. Новые тесты добавлять только для критичных исправляемых регрессий;
существующие race/vet/vuln/module/RPC и driver lint gates сохраняются.
Не добавлять новые benchmark campaigns и test frameworks для этого этапа.

Текущий batch: timeout считывается из запроса до minimum clamp; SDK filter
отклоняет nil filter, отсутствующий/null/non-array entities и non-object element
с ошибкой вместо panic. Корректные filter payload и optional queue/RPC JSON
контракты сохраняются. Остальная валидация YAML/hardware, bounded response
bodies и безопасная диагностика остаются открыты в Beads; этот batch их не закрывает.

Go release notes проверены через GitHub API
[`golang/website` — Go 1.27](https://go.dev/doc/go1.27), source blob
`2d38a2be7836deca1d17451c6e03dd444f282847`; stdlib HTTP body ownership проверен
через Context7 `/golang/go`. Новые зависимости и версии tools для этих
исправлений не требуются. Driver использует неизменяемый pin исправленного SDK.

SonarCloud Web API `issues/search`, PR #1, 2026-10-15: security gate сообщает
`docker:S6471` для root в dev/fixture containers и `go:S4036` для PATH тестового
helper driver; `go:S3776` в SDK transport test относится к cognitive complexity.
`docker:S8431` рекомендует убрать сочетание tag+digest; обязательный immutable
pin с читаемой версией сохраняется. Findings зарегистрированы в
`um-docs-nrsu.6.10.6`; suppression и отключение checks не выполняются.


Проверка текущего driver dependency graph через `govulncheck -show verbose`:
0 symbol-level, 0 package-level findings; module-only `GO-2026-5932` относится
к неимпортируемому `golang.org/x/crypto/openpgp`, исправленной версии нет.
SSH-зависимость x/crypto не объявляется уязвимым исполненным путём по наличию
другого пакета в module. Advisory сохраняется в ограничениях supply-chain audit.
