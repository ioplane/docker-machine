# План доработки Nutanix node driver

Дата: 2026-10-15. Форк: `ioplane/docker-machine`. База:
`nutanix/docker-machine@33d066f7174aea9d33a59642b12394dc0c890930`.
Источник задач — локальный Beads в `um-docs`, программа `um-docs-nrsu`.
Этот документ определяет последовательность и критерии, но не заменяет статусы Beads.

## Границы поставки

Первый этап включает обновление инструментов и зависимостей, строгую статическую
проверку и передачу числа сетевых очередей в запрос создания VM.
Регистрация нового driver в Rancher, изменение шаблонов и создание VM выполняются
отдельно после проверки provider и модулей. Запуск нагрузочных испытаний и изменение
рабочих кластеров в этот этап не входят.

Полная совместимость означает проверенный набор операций и полей для конкретных
версий Rancher, Prism Central и AHV. Обновление SDK не добавляет поля автоматически
в CLI, DynamicSchema, Terraform provider или Terragrunt inputs.

## Приоритеты и последовательность

| Этап | Beads | Критичность / приоритет | Изменение и назначение | Критерий завершения |
|:---|:---|:---|:---|:---|
| База | `um-docs-nrsu.6.10.1` | C3 / P1 | Форк через GitHub API; актуальные stable Go и SDK; проверенный Rancher machine; фиксированные зависимости и образы | Воспроизводимая сборка, чистый tidy, race/vet/vuln без ошибок; описаны ABI и ограничения |
| Проверки | `um-docs-nrsu.6.10.2` | C3 / P1 | golangci-lint v2, все применимые линтеры, строгая проверка ошибок, форматирование и CI | Schema validation и полный lint без находок; исключения локальны и обоснованы |
| Очереди | `um-docs-nrsu.6.10.3` | C3 / P1 | Optional StringFlag, проверка диапазона, SDK `VMNic.NumQueues` для каждого создаваемого NIC | Тесты nil/default, ошибочных значений, UUID/VLAN/OVERLAY и RPC/state roundtrip |
| Изоляция SDK | `um-docs-nrsu.6.10.4` | C2 / P1 | Fork `ioplane/prism-go-client`: отдельный HTTP transport и TLS/proxy для каждого клиента | Concurrent regression RED→GREEN, unchanged global transport, точный pin; PR #1 и maintainer review |
| Provider и модули | `um-docs-nrsu.6.4` | C3 / P2 | `rancher/terraform-provider-rancher2`: schema/expand/flatten; `infra-modules/modules/rancher-nutanix-rke2-cluster`: contract/adapter/high_level/main | Значение проходит всю цепочку; отсутствие параметра не меняет template hash; unsupported input отклоняется |
| Образ и гостевая ОС | `um-docs-nrsu.6.5` | C3 / P2 | `infra-images` и `infra-ansible`: ролевые настройки каналов и IRQ после определения NIC/CPU | Воспроизводимый cold boot; реальные maximum/current channels; без универсального набора sysctl |
| Canary | `um-docs-nrsu.6.6` | C3 / P2 | `infra-live`: неизменяемые версии и checksum, отдельный разрешённый pool | Фактический driver hash, PC queues, guest channels, ошибки/дропы/softirq, throughput и p99; rollback проверен |
| Lifecycle | `um-docs-nrsu.4.8` | C2 / P1 | Task polling, timeout, retry/idempotency, безопасная компенсация, выбор management IP | Контрактные тесты transient errors, malformed responses, multiple NIC/IP и неизвестного исхода create; без удаления чужой VM |
| Аппаратные профили | `um-docs-nrsu.4.7`, `um-docs-nrsu.7` | C2 / P1 | Versioned capability profile: CPU topology, vNUMA, passthrough, memory overcommit, storage и placement | Одно декларативное владение; проверенная поддержка поля API; профиль соответствует нагрузке и ограничениям миграции/HA |
| Безопасность | `um-docs-nrsu.10.1` | C2 / P1 | Отдельная API identity, минимальные права, TLS, отсутствие секретов в логах | Negative authorization tests, ротация и отсутствие credential leakage |
| Приёмка | `um-docs-nrsu.9.3` | C2 / P1 | Полный BOM, HA/N−1, recovery, storage/CNI и операционные процедуры | Измеренные SLO, восстановление и отказоустойчивость; успешный CI не заменяет приёмку |

C2 обозначает потенциальные последствия для доступности или безопасности;
C3 — производительность и управляемость. Это классификация риска работ,
а не подтверждение текущего инцидента.

## Контракт сетевых очередей

`nutanix-vm-nic-queues` — строковый optional flag с пустым значением по умолчанию.
Пустая строка сохраняет прежний payload: `num_queues` отсутствует. Явное значение
должно состоять из десятичных цифр, быть положительным и не превышать
`nutanix-vm-cpus × nutanix-vm-cores`. Переполнение отклоняется.

Значение применяется ко всем создаваемым NIC, включая дополнительную сеть.
Per-NIC профили и изменение существующей VM не входят в первый patch.
Проверка выполняется до обращения к Prism, в том числе при восстановлении driver
из сохранённого JSON. Старый JSON без нового поля остаётся допустимым.

Тестирование включает отрицательные значения, ноль, дробь, знак `+`, пробелы,
нечисловую строку, переполнение и превышение общего числа vCPU. HTTP fixture
проверяет фактический SDK JSON, а не только внутреннюю структуру Go.

## Проверка совместимости

Проверять три разных контракта:

1. Исполняемый файл: целевые OS/architecture, отсутствие динамического CGO runtime
   в release binary и воспроизводимость `-trimpath`.
2. Rancher: subprocess/RPC handshake, `GetCreateFlags`, тип и default нового поля,
   `SetConfigFromFlags`, JSON roundtrip, сохранение `VMId` и существующих полей.
3. Nutanix: используемая версия API, HTTP method/path, nullable поля и обработка
   task UUID. Модуль Go с суффиксом версии не определяет версию Prism API.

Бинарная совместимость Go shared plugin не является критерием для самостоятельного
node-driver процесса. Проверка RPC должна подтвердить фактический транспорт;
успешная компиляция сама по себе этот контракт не подтверждает.

## Источники и фиксация версий

- Версии и состояние репозиториев: `gh api graphql` и metadata Go module channel.
- Go stable и release notes: <https://go.dev/dl/> и <https://go.dev/doc/devel/release>.
- Контракт Go: <https://go.dev/doc/go1compat>.
- golangci-lint v2: Context7 `/golangci/golangci-lint`,
  <https://golangci-lint.run/docs/configuration/> и reference YAML выбранного тега.
- Rancher machine: Go module, RPC client/server и plugin sources выбранного SHA;
  сравнение с зависимостями конкретного Rancher выполняется до замены.
- Prism SDK: <https://github.com/nutanix-cloud-native/prism-go-client/releases>.
- Предыдущий RO audit и vendor-qualified ограничения:
  `um-docs/docs/infrastructure/15-internal/rke2-vnic-queues/` и
  `um-docs/docs/infrastructure/15-internal/rke2-enterprise/`.

Все проверки разработки выполняются внутри объявленного Podman dev container.
Обновление CI не запускает release и не регистрирует driver в Rancher.
