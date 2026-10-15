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
`github.com/ioplane/prism-go-client v0.8.3-0.20261015125745-70ca9c60ff06`,
commit `70ca9c60ff066dfa3ee00977d0f4c27998874245`,
[PR #1](https://github.com/ioplane/prism-go-client/pull/1).
Это исправление поверх `v0.8.2`, а не опубликованный upstream release `v0.8.3`.
Причина: upstream SDK изменяет общий `http.DefaultTransport` при создании клиента.
Параллельный regression-тест воспроизводит гонку и смешивание TLS/proxy-настроек.
Fork клонирует transport для каждого клиента и возвращает ошибку при неподдерживаемом
типе default transport. Тестовая параллельность сохранена. Дополнительно SDK filter возвращает ошибку
для nil filter и неверной формы entities вместо panic; корректные ответы
и импортируемые сигнатуры сохраняются. Общий HTTP boundary ограничивает network
JSON до 64 MiB и error body до 64 KiB. ResponseError.StatusCode и Unwrap сохраняют machine-readable status
и причину; ErrResponseTooLarge определяется через errors.Is. Server body,
message/details и произвольный HTTP status text исключены из обычного Error/log.
Нефильтрованный io.Writer остаётся потоковым. Крупные inventory используют
пагинацию; прежние message-string assertions заменяются на errors.As/Is.
Это breaking diagnostics/size policy SDK, не ограничение Nutanix API.

Public API сравнен по 15 packages W0: 14 без изменений; root аддитивно содержит
новый error type/sentinel и методы. Credentials, VM DTO, nil/omitted fields
и v4/converged categories не изменены. W0 text hash исторический, binary Go ABI
из этого сравнения не следует.

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

## Входные данные Create: этап v5

CPU sockets и cores положительны, их произведение помещается в int64 независимо
от queue flag; RAM MiB положительна. Необязательные image/disk GiB равны нулю
для прежнего omission либо положительны без переполнения целевого int64 поля
MiB/bytes. Это границы представления, не аппаратные максимумы Nutanix.
Проверка выполняется при SetConfigFromFlags и повторно перед Create для
восстановленного RPC/JSON state. Имена, типы и JSON tags публичных полей сохранены.

Непустой cloud-init начинается строкой `#cloud-config` и содержит один YAML
mapping; `users` — только прямой sequence. Вложенные `users` сохраняются.
Существующий root user получает bootstrap key без дубликата записи или ключа;
его остальные поля, sudo и ключи сохраняются. При отсутствии sudo добавляется
прежнее правило bootstrap. Строковый root преобразуется в mapping; default и
остальные пользователи сохраняются. Дубли root, неоднозначные mapping keys,
alias/merge в изменяемых узлах и неверная форма root SSH keys отклоняются.
Пустой userdata сохраняет прежний default payload. Декодирование и SSH key
подготовка выполняются до Prism calls; parser errors не раскрывают raw userdata.
Это проверка bootstrap boundary, не полная cloud-init schema или guest boot.

Переход: заменить alias/merge для root/users/SSH keys явными узлами и устранить
дубли/неверные числовые значения. Ранее такие inputs могли дойти до API;
теперь возвращается ошибка до запроса. SDK pin и queue payload не меняются.


## Upload dependency: этап v6

Текущий immutable pin — SDK `70ca9c60ff066dfa3ee00977d0f4c27998874245`,
`v0.8.3-0.20261015125745-70ca9c60ff06`. HTTP budgets/errors из v4 сохраняются;
дополнительно исправлено владение file upload body и независимость replay.
Public entry points, driver RPC/JSON и VM NIC queues сохраняются. Low-level
upload callers должны держать regular file открытым и неизменным до завершения
запроса/повторов; EOF даёт пустой body, pipe/device/directory отклоняются.
Go API/wire совместимость не является утверждением о binary ABI или live Prism.


## Deadline и NIC identity

Один положительный operation timeout покрывает HTTP/body/task/IP/power;
Restart разделяет этот бюджет между OFF/ON. CLI минимум300s сохранён; invalid
restored timeout отклоняется. Failed/aborted task и malformed response не дают
success; power task требует observed power state. Автоматического удаления VM
из-за create/task/IP ошибки нет. Root и isolated Testcontainers используют
одинаковый immutable SDK pin; CI проверяет replacement parity.

NIC сохраняют порядок профиля. Имена с несколькими подходящими VLAN/overlay
подсетями требуют UUID; повторные subnet UUID/aliases отклоняются. Management
IP берётся с единственной NIC первой заявленной подсети. Порядок endpoint/NIC
в ответе не задаёт identity; IPv4 имеет приоритет при выборе допустимых адресов.
Это совместимость формата RPC/config, а не обещание принятия неоднозначного
старого профиля или end-to-end IPv6 support.


Create с известным VMId не подаёт новый POST. Независимый журнал
`StorePath/machines/MachineName/nutanix-create.json` сохраняет stable allocator
client ID до allocator POST, выделенный VM UUID, cluster, management subnet,
task и фазу операции. Хеш намерения связывает endpoint, machine, профиль VM,
cloud-init, SSH user/password и содержимое публичного SSH key. Секреты,
публичный ключ и raw userdata в журнал не записываются; consumer config.json
не перезаписывается. До API проверяются читаемость и формат обоих SSH keys,
соответствие private/public pair; отсутствующий, повреждённый или чужой private
key блокирует восстановление. Потерянный allocator response восстанавливается через GET
того же client ID; после VM POST возможен только GET точного выделенного UUID.
GET404 после неопределённого VM outcome не разрешает повторный POST.
Remove также сохраняет этот журнал и возвращает ошибку при GET404, пока
нет terminal create task: позднее завершение исходного POST ещё возможно.

PreCreateCheck выделяет UUID и подготавливает SSH files до Machine Save,
архивирующего machine directory в Secret. Разрешение единственного VM POST
остаётся в памяти текущего ActualDriver; GetConfigRaw его не сериализует.
Восстановленный checkpoint reserved также считается unknown: разрешён только
GET выделенного UUID, даже если исходный процесс не успел подать POST.
Прямой Create без hook поддерживается при сохранении локального store.

Breaking: для операций требуется сохраняемый StorePath на Linux с локальными
flock, atomic rename и fsync; Create дополнительно требует поддержки и доступа
к POST/GET Prism v3 idempotence allocator. Небезопасного fallback нет.
Каталоги принадлежат effective UID и недоступны для чужой записи; журнал и
постоянный lock inode — private regular files. Требуются сохранение machine
directory и исходного bootstrap key. Неверный binding, повреждение,
symlink/hardlink либо небезопасные права блокируют API. После перезапуска
проверяются UUID, имя и cluster VM; IP выбирается по сохранённой
management-подсети. Отсутствие IP не вызывает удаления.
Локальный restart сохраняет StorePath; замена процесса может восстановить
тот же pre-Create machine checkpoint с журналом и ключами. TC проверяет
driver-level restore копии старого checkpoint в новый StorePath после SIGKILL
с явным переносом SSHKeyPath в test fixture. Это
этап `um-docs-nrsu.4.8.1.1`; реальный Rancher CLI отказывает при Exists(name)
до driver hook. Автоматический consumer resume и приёмка Job/pod/controller
остаются `um-docs-nrsu.4.8.1.2`; родительская `um-docs-nrsu.4.8.1` не закрыта.

Allocator вызывается с count=1 и valid_duration_in_minutes=1440.
Опциональный client_identifier в ответе может отсутствовать; непустое чужое
значение отклоняется. Driver намеренно требует future expiration_time перед
VM POST, хотя Swagger помечает поле опциональным: отсутствие безопасного
срока действия даёт fail-closed без VM mutation. Источник —
[закреплённый v3 Swagger](https://github.com/ioplane/prism-go-client/blob/70ca9c60ff066dfa3ee00977d0f4c27998874245/v3/swagger.json#L17731).

Remove восстанавливает UUID из журнала и проверяет exact VM metadata; GET404
даёт success без DELETE при подтверждённом terminal create outcome. После
DELETE требуется observed absence; повторный
Remove идемпотентен. Legacy config с VMId и без журнала сохраняет прежний путь
при действительных StorePath/timeout. Create/Remove/power и GetState
сериализуются по machine store, включая разные процессы. Configuration остаётся
неизменной от PreCreateCheck до возврата Create; чтение bootstrap IP выполняется
после неё, как в закреплённом
[Machine consumer](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/libmachine.go#L117).
Одновременная внешняя запись exported fields не поддерживается.
Источники consumer checkpoint: [Secret Save](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/persist/secretstore.go#L65),
[RPC marshal](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/libmachine/drivers/rpc/client_driver.go#L201),
[CLI Exists guard](https://github.com/rancher/machine/blob/407bfdc12e6fdfd19c9d76d565d552ea5b39f363/commands/create.go#L218).
