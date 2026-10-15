# Контракт совместимости

Дата проверки исходников: 2026-10-15.

## Rancher и исполняемый driver

Исходный upstream использует архивированный `github.com/docker/machine v0.16.2`.
Выбран сопровождаемый `github.com/rancher/machine v0.15.0-rancher145.2`, SHA
`407bfdc12e6fdfd19c9d76d565d552ea5b39f363`: именно эту версию использует
[Rancher v2.15.2](https://github.com/rancher/rancher/blob/272e65580ca92b9dc51e0f0f0f9ae9d6d356e3b8/go.mod#L129).
Это выбор канала совместимости с потребителем. Более высокий SemVer старой
ветки Machine и RC другого канала не являются автоматическим обновлением.

Node driver — отдельный процесс. Его совместимость определяется plugin handshake,
RPC и JSON, а не Go shared-object ABI. `RegisterDriver` после нормализации imports
совпадает с прежним; API protocol version равна `1`, config version — `3`.
Основные RPC signatures и структуры флагов сохранены.
Источники: [plugin registration](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/drivers/plugin/register_driver.go),
[RPC server](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/drivers/rpc/server_driver.go),
[version constants](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/version/version.go).

Известные различия:

- `BaseDriver` добавляет `IPv6Address` и `GetIPv6`; старый JSON читается без этого
  поля, новый JSON может содержать дополнительное поле.
- `BoolFlag.Default()` возвращает `false` вместо прежнего `nil`. Сами типы флагов
  и их имена сохраняются; отсутствие строкового queue flag остаётся пустой строкой.
- Текст ошибки пустого `GetIP` изменён на `IPv4 address is not set`.
- Замена `github.com/urfave/cli` сохранена по контракту Rancher Machine: его
  [go.mod](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/go.mod#L7)
  предупреждает о нарушении RPC binding при неконтролируемом обновлении CLI.

Импорты Machine переводятся согласованно; `replace` старого module path на новый
не используется. Public module path самого node driver сохранён, чтобы не менять
импорты потребителей и production driver name `nutanix`.

## Prism SDK и JSON API

База Prism SDK — стабильная версия `v0.8.2`. В `go.mod` закреплён fork
`github.com/ioplane/prism-go-client v0.8.3-0.20261015010339-eefdc324a6c3`,
commit `eefdc324a6c3989ef60f54cc8a68fdce947d7366`,
[PR #1](https://github.com/ioplane/prism-go-client/pull/1).
Это исправление поверх `v0.8.2`, а не опубликованный upstream release `v0.8.3`.
Причина: upstream SDK изменяет общий `http.DefaultTransport` при создании клиента.
Параллельный regression-тест воспроизводит гонку и смешивание TLS/proxy-настроек.
Fork клонирует transport для каждого клиента и возвращает ошибку при неподдерживаемом
типе default transport. Тестовая параллельность сохранена.

Используемый запрос остаётся
`POST /api/nutanix/v3/vms`, поле — `spec.resources.nic_list[].num_queues`.
SDK задаёт его как nullable `*int64` с `omitempty`:
[VMNic](https://github.com/nutanix-cloud-native/prism-go-client/blob/v0.8.2/v3/v3_structs.go#L39),
[CreateVM](https://github.com/nutanix-cloud-native/prism-go-client/blob/v0.8.2/v3/v3_service.go).

Context7 использован для маршрутизации и общего контракта. Конкретные имена типов
и полей проверяются по исходникам выбранного тега: автодокументация Context7 содержит
примеры с другими именами VM spec и не является основанием для изменения payload.

Пустой queue flag оставляет SDK pointer равным nil. Изменение существующей VM,
горячая смена очередей и переход всего driver на API v4 этим patch не реализуются.
Поддержка конкретного AHV build и поведение гостевой ОС требуют отдельной приёмки.

## Проверки и оставшиеся границы

Go: language floor `1.27.0`, toolchain `go1.27.2`. Golangci-lint `2.14.0`:
`default: all`, 112 включённых линтеров, три отключённых устаревших предшественника.
`gocritic`, `govet`, `staticcheck` и правила `revive` включены полностью; исключения
для optional SDK fields, прежнего JSON API, public package name и декларативной
таблицы флагов перечислены в `.golangci.yml`. Ошибки и security-проверки в тестах
не исключены целиком. Ограничения сложности и длины строк заданы в конфигурации;
термин «все линтеры» не означает отсутствие обоснованных исключений правил.

`machine/main_test.go` проверяет настоящий driver subprocess: version handshake,
flags, конфигурацию и сохранённый JSON. `machine/driver/queues*_test.go` проверяют
валидацию и реальный HTTP JSON SDK для UUID/VLAN/OVERLAY без подключения к Nutanix.
HTTP fixture останавливает запрос на create и не имитирует успешный lifecycle VM.

Эти проверки не подтверждают live registration, DynamicSchema cache,
Terraform expand/flatten, template hash, поддержку версии AHV, throughput или p99.
Такая интеграция проходит отдельную приёмку по плану и разрешённому canary.
