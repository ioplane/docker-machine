# Nutanix node driver: план модернизации v12

Дата: 2026-10-15. Источник задач — локальный Beads `um-docs-nrsu` в um-docs.
[Полный план, инфраструктурные эпики и критерии](https://um-git.infra4.dev/p.lavrukhin/um-docs/-/blob/sprint/nutanix-premerge-2026-10-15/docs/plans/nutanix-rke2-modernization/README.md).

Канонический v12 закрывает input/HTTP/auth/deadline/NIC prerequisite batch;
источник приёмки — его общий индекс и exact-head receipts. Следующий активный
scope — `um-docs-nrsu.4.8.1.1`: driver journal/checkpoint. Parent `um-docs-nrsu.4.8.1`
остаётся открытым до consumer resume `um-docs-nrsu.4.8.1.2`. Секции v3–v11 ниже
содержат исторические снимки этапов; актуальные ограничения C1 приведены в v12.

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
контракты сохраняются. HTTP продолжен этапом v4 ниже; YAML/hardware — этапом
v5. Статусы review/delivery и оставшаяся программа ведутся в Beads.

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


## HTTP boundary v4

На этапе v4 driver закреплён на SDK `6ee1b0d01b4e362ee4c929a77893ac7810b81a48`, Go module
`v0.8.3-0.20261015100321-6ee1b0d01b4e`, разрешённом через go list и GitHub API.
JSON/error body budgets: 64 MiB/64 KiB, application read максимум budget+1.
ResponseError.StatusCode и errors.Is/As заменяют server-message matching;
обычные errors/logs не раскрывают response payload. Unfiltered io.Writer
остаётся потоковым; исходный body закрывает его первоначальный владелец.

Изменение SDK помечено breaking; wire/RPC/config поля driver сохраняются.
Текущие consumer/RPC/Testcontainers gates проверяют новый pin. Новых driver
features, tests, dependencies tools и performance campaigns для pin нет.
Следующий runtime scope после input batch v5 — auth/replay/deadline;
merge/release/live acceptance остаются отдельными gates.

## Входные данные: этап v5

`um-docs-nrsu.4.8.4`, P1/C2: hardware validation охватывает CLI и восстановленный
Create state независимо от queues; int64 conversions проверяются до умножения.
Cloud-init вынесен в `machine/driver/cloudinit.go` внутри существующего package;
рекурсивный поиск users заменён прямым mapping lookup, root merge повторяем.
Невалидный YAML/hardware input не достигает Prism. Новых dependencies,
frameworks, exported API или queue flags нет; критичные regressions используют
прежний TLS fixture. Полная schema/guest/canary приёмка остаётся отдельным этапом.

Источники: Context7 `/yaml/go-yaml` и `/canonical/cloud-init`, точный
[yaml.v3 source](https://github.com/yaml/go-yaml/blob/e16c7af9361b241fa02d91582fb59ce4954d8afc/decode.go#L484)
и [Decoder](https://github.com/yaml/go-yaml/blob/e16c7af9361b241fa02d91582fb59ce4954d8afc/yaml.go#L120),
[cloud-init users](https://github.com/canonical/cloud-init/blob/main/doc/rtd/reference/yaml_examples/user_groups.rst).
GH GraphQL подтвердил v3.0.5 tag; published GitHub Release отсутствует,
compare v3.0.4...v3.0.5 изучен. API новой yaml main (`WithUniqueKeys`) не
переносится в установленный v3: Node bypasses mapping decode validation.
Go 1.27 release notes повторно сверены через GH GraphQL, source blob
`2d38a2be7836deca1d17451c6e03dd444f282847`; generic methods не нужны для
этого исправления. Аппаратные vendor limits и guest compatibility не измерены.

Следующая очередь: review/delivery input и HTTP patches, затем auth/replay и
общий deadline по существующим Beads prerequisites; далее NIC/IP identity и
operation reconcile. Full SDK stylistic quality остаётся отдельным W2/rank90.


## Upload SDK pin: этап v6

Driver использует SDK `e7f87ddb90ec2631d0cbe7ba3936c3e90946cf5b`, Go module
`v0.8.3-0.20261015110003-e7f87ddb90ec`, подтверждённый через go list/GH API.
Upload builders читают текущий диапазон caller-owned regular file через
независимые SectionReader; повтор после Body.Close не использует закрытый
файл, Content-Length соответствует оставшимся bytes. v3 Operations.UploadImage
сохраняет свой defer Close. Новых retries, API fields и queue payload изменений нет.
Этап 4.8.7 остаётся in_progress: session refresh/cancellation и cache ещё открыты.
Полный SDK strict quality остаётся отдельным W2/rank90; новых driver tests нет.


## Session SDK pin: этап v7

Driver использует SDK `cfe9dabe248b1cf4d3dc84a83ec71589640a88ac`, module
`v0.8.3-0.20261015113410-cfe9dabe248b`, разрешённый через go list/GH API.
Общий v3/Konnector HTTP client синхронизирует session cookies, разделяет один
refresh между одновременными401 одной session version и применяет одинаковый
single-replay contract в Do и DoWithFilters. Отдельный ожидающий может отменить
context; общий refresh ограничен context владельца. Non-replayable body,
transport errors с неизвестным результатом и auth/GetBody errors повтор ресурса
не запускают. Первая отправка сохраняет unauthenticated caller headers; session cookies
заменяются только после401. Заранее построенные запросы со старой cookie
используют уже завершённый refresh. Исходные response bodies
закрываются по одному разу. Driver basic/API-key путь, RPC/config и queue JSON
не меняются; session auth не включается принудительно.

Критичные regressions находятся в SDK:8 concurrent requests/2auth/16resource
calls, replay bytes, cancellation, single retry, errors и body ownership.
Новых driver tests/dependencies/tools нет. Действующие driver make check,
strict lint, RPC и rootless Testcontainers выполняются на текущем consumer pin.
Constructor context, v3/v4 concurrent cache, общий deadline и operation reconcile
остаются открыты; um-docs-nrsu.4.8.7 не закрывается по этому подэтапу. Нет
merge/release/Rancher registration или live provision/performance acceptance.


## Context/cache SDK pin: этап v8

Driver закреплён на SDK `70ca9c60ff066dfa3ee00977d0f4c27998874245`, module
`v0.8.3-0.20261015125745-70ca9c60ff06`, origin проверен через Go metadata/GH API.
SDK добавляет context-aware v3/Konnector constructors и v3/v4 cache entrypoints;
старые entrypoints сохраняют Background. Кэш объединяет создание на key, не
держит mutex во время I/O и сохраняет RLock для cache hit. Неуспешная replacement
сохраняет прежнюю запись; Delete запрещает позднюю публикацию старого constructor.
Credentials/TLS/trust входят в hash; functional options первых успешных publications
действуют до endpoint change/Delete. Только SDK-owned v3 idle transports закрываются
при retirement; caller и generated v4 transport ownership не меняется.

Критичные SDK regressions выполняются с race: 8 callers/1 constructor для v3/v4,
constructor auth cancellation, waiter cancellation, rotation/failure/Delete и
options ownership. TLS fixture v3 измеряет 1 auth и 1 TLS connection для 8 callers.
V4 option errors сохраняют cause; новых dependencies/frameworks нет.

Driver Go/RPC/config/VM queue payload и auth defaults в этом pin batch не меняются.
Новые constructor entrypoints ещё не задают общий deadline драйвера: это следующий
um-docs-nrsu.4.8.2 с явным threading operation context. Обязательные make check,
strict lint/RPC и rootless Testcontainers проверяют текущий consumer; full SDK
strict quality/Sonar и merge/release/live acceptance остаются отдельными gates.


## Deadline и автомат задач: этап v9

`um-docs-nrsu.4.8.2`: один deadline покрывает constructor auth, lookup/submit,
HTTP response body, task polling и ожидание IP; обе фазы Restart используют
тот же бюджет. Сохранённые timeout ≤0 и переполнение Duration отклоняются до API.
CLI сохраняет прежний минимум 300 секунд. Context не входит в RPC/config JSON.

Источник v3: pinned SDK `v3/swagger.json`, `definitions.task.properties.status`:
QUEUED/RUNNING/SUSPENDED ожидаются; SUCCEEDED завершает task; FAILED/ABORTED
возвращают ошибку. Отсутствующий или неизвестный status отклоняется; CANCELED из
v4 не переносится в v3. Raw error_detail в диагностику не выводится.
Context7 `/golang/go`: context.WithTimeout, HTTP body cancellation и synctest;
Nutanix MCP TN-2209 содержит v4 guidance и не подтверждает enum v3.

Повторяются только GET-наблюдения с HTTP 429/500/502/503/504, с jitter 5–10 секунд
внутри общего бюджета. 403/404, ошибки схемы и transport outcome возвращаются
без повторной подачи mutation. После power task дополнительно проверяется
status.resources.power_state. Отмена либо исчерпание срока не означают отмену
операции на сервере: VMId сохраняется, автоматического DeleteVM нет.
Durable task identity и reconciliation после перезапуска — отдельный4.8.1;
первый NIC пока сохраняет прежнюю роль, identity selector — следующий4.8.3.

RED: три invalid persisted timeout сделали по4 API requests в старом коде.
Критичные проверки включают synthetic shared task/IP budget7s, terminal/unknown/
missing/null task, 403, transient retry, throttling deadline, cancellation,
TLS body stall и observed power state. Полная проверка и CI фиксируются отдельно
по опубликованному SHA; merge, release и live changes не выполняются.


Проверка nested module выявила прежний pin Testcontainers
`v0.8.3-0.20261015010339-eefdc324a6c3`, тогда как root module уже использовал
`v0.8.3-0.20261015125745-70ca9c60ff06`. Поэтому прежние TC receipts не доказывают
интеграцию нового SDK. Pin выровнен; integration-mod-check сравнивает replacement
Path/Version обоих module graph перед tidy/verify/compile. Требуется новый TC
прогон по итоговому driver SHA.


## NIC/IP identity: этап v10

`um-docs-nrsu.4.8.3`: NIC payload сохраняет порядок входного профиля. Имена
разрешаются однозначно в целевом cluster для VLAN; совпадающие overlay names
между VPC требуют явного UUID. Literal UUID используется как reference без
дополнительного inventory request; существование и область проверяет Prism при
Create. Duplicate/alias UUID, пустые профили и unresolved/malformed candidates
отклоняются до Create. NicList присваивается атомарно после полной проверки.

Management selector — UUID первой заявленной подсети, переданный в пределах
Create operation. Ответ может менять порядок NIC: индекс не используется.
Две NIC той же management-подсети — ошибка неоднозначности; disconnected NIC
не выбирается. nil/empty endpoints безопасны; invalid IP возвращает ошибку,
loopback/link-local/multicast/unspecified/scoped адреса исключены. Адреса
нормализуются net/netip, IPv4 предпочитается IPv6, затем выбирается наименьший
адрес. IPv6 parsing не доказывает end-to-end dual-stack Rancher/Prism support.
Raw IP/response payload в ошибку не выводится.

Новых CLI/RPC/config fields нет. Существующие профили с неоднозначными names или
несколькими NIC одной подсети требуют устранения неоднозначности. Persisted
selector и recovery после process restart остаются4.8.1; отдельный MAC/NIC UUID
selector не добавлен без подтверждённого consumer/API контракта.

RED name-first/UUID профиль выявил UUID-first reorder. Критичные проверки
охватывают порядок payload, VLAN/VPC ambiguity, alias/casing, nil/malformed
inventory/endpoints, reordering management NIC и IPv4/IPv6 parsing.
Testcontainers queues fixture использует name-first/UUID порядок и проверяет
subnet UUID каждой NIC; остальные boundary scenarios сохраняются.


## Reconcile: журнал операции v12

`um-docs-nrsu.4.8.1.1`: private per-machine journal фиксирует client ID до
allocator mutation, затем UUID/subnet/cluster до VM POST и task до ожидания.
Atomic file write, file/directory fsync и постоянный flock inode защищают
публикацию и сериализацию. После interrupted allocator POST используется GET
того же client ID. После неопределённого VM POST повторной mutation нет:
выполняется exact UUID/name/cluster readback и выбор IP сохранённой подсети.
GET404 не доказывает, что исходный POST не выполнится позже; автоматический
новый POST и удаление из-за отсутствия IP запрещены. Remove допускает
повторный observed404 и проверяет владение перед DELETE.

PreCreateCheck готовит reserved UUID/journal и SSH key до Machine Save;
памятная attestation разрешает текущему процессу подать единственный VM POST.
Restored reserved checkpoint права на POST не содержит и допускает только
GET точного UUID. Pinned Rancher Machine
`libmachine/libmachine.go:117–161` сохраняет host до Create и после success;
после ошибки отдельное сохранение предусмотрено только для vmwarevsphere.
Nutanix VMId, изменённый в failed RPC Create, не получает этим путём гарантии
постоянного хранения. `rpc/server_driver.go:107–115,133–142`: GetConfigRaw —
отдельный marshal без замены ActualDriver, а Create возвращает ошибку. Secret
store Save архивирует machine directory, включая подготовленный журнал и ключи.
Context7 `/rancher/rancher` не
содержит ответа; вывод основан на точном dependency source407bfdc12e6fdfd19c9d76d565d552ea5b39f363.

Pinned v3 Swagger: public POST/GET `/idempotence_identifiers` выделяет UUID4
по client_identifier; произвольный UUID по умолчанию не принимается в Create.
`/idempotence_identifiers/salted` помечен x-internal и предупреждает о UUID5
коллизиях между environments; v4 NTNX-Request-ID не переносится в v3.
SDK уже содержит allocator operations; новые SDK wrappers, dependencies,
CLI/RPC/config fields не требуются. Binding содержит digest bootstrap identity;
при восстановлении ключ не генерируется заново. Неизвестная, повреждённая или
чужая запись блокирует API. Требуются Linux, сохраняемый local machine store,
allocator permission и future expiration_time; пустой client identifier echo
разрешён. Breaking-ограничения — [совместимость](compatibility.md).

Критичные проверки используют существующие HTTP fixtures и Testcontainers:
потеря allocator/VM response, failed task, IP timeout, unknown404, чужая VM,
повторный Remove, late Create, файловая граница и конкурирующий Create. TC
сохраняет checkpoint до VM POST, теряет ответ, убивает дочерний процесс и
восстанавливает старые machine files в replacement store. UUID/IP
восстанавливаются при единственных allocator POST и VM POST. Этап требует
exact-head CI и отдельные SPEC/quality reviews. CLI Exists(name) блокирует
автоматический повтор create до driver hook; consumer opt-in resume относится
к `um-docs-nrsu.4.8.1.2`. Parent C1 остаётся открытым; live Prism, production
registration и canary не входят в этот driver stage.
