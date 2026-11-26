# 牧区合作社断电恢复编排器

停电后几十台服务器同时开机、缓存全空、终端齐连，会把数据库打垮。这个项目用 Go 离散时间模拟了一个确定性的恢复编排器，并带一个无外部依赖的网页：

- 每台机器何时上电、引导完成、硬件故障、重启环识别、隔离和漂移；
- 每个服务何时开始启动、端口打开、执行**真实就绪检查**、预热、核对、开始接流量；
- 发电机、市电切换、UPS 充电/放电和十分钟电压观察窗；
- 收购点终端收到 `503 + Retry-After` 后错峰重连；
- 数据库/缓存 QPS 与压力曲线、UPS 电量、发电机负载；
- 每个等待中的服务具体卡在 UPS、电压、依赖就绪、错峰槽位还是压力余量；
- 停电前未结收购单核对，异常单进入人工队列；
- 固定种子生成 64 种规模/依赖/故障组合，并与逐 tick 推演的参照实现逐项比较时间线。

## 运行

```bash
# 网页，默认 :8080（仓库根目录可直接 go run .）
go run . -web -addr :8080
# 等价：go run ./cmd/shepherd -web -addr :8080；或 make run

# 固定种子验收：64 个场景
go run . -acceptance -runs 64

# Go 测试（含时间线逐项一致性和八条业务规则断言）
go test ./... -count=1

# 只看一个场景摘要
go run ./cmd/shepherd -sim 7
```

网页打开 <http://localhost:8080>。可以改场景编号和种子基数，也可以直接访问：

- <http://localhost:8080/?variant=7>
- <http://localhost:8080/?variant=0&problems=1>
- <http://localhost:8080/?accept=1>

API：

- `GET /api/sim?variant=0&seed=20261127`
- `GET /api/acceptance?runs=64`

## 验收口径

`internal/acceptance` 会对每个固定种子场景执行主编排器和 `internal/reference` 中的朴素参照推演器，并比较规范化后的 `[]Item` 时间线（时间、类型、对象、标签、详情完全一致）。同时检查：

1. 所有服务最终启动、核对（如需要）并接流量，所有终端批次重连完成；
2. 每个采样点数据库和缓存压力均不超过 `PressureHardLimit = 90%`；
3. 发电机供电期间负载不超过额定 kW；
4. 首次启动严格晚于依赖的首次真实就绪；再次停电导致依赖重置后，最后一次启动也严格晚于依赖当前就绪点；
5. 有未落盘数据的服务不会在电压稳定前启动；
6. 非关键服务不会在 UPS 充到“可撑过一次正常关机”的 80% 前启动；
7. 结算服务只有在未结收购单核对完成后才放外部流量；
8. 终端只在终端网关和结算链路放流后接入；
9. 重启环机器被隔离，服务迁到指定漂移目标；坏盘机器切到预指定备用机并核对最后同步版本；
10. 来电后的总恢复时间不超过 3600 秒。

最近一次本地 `go run ./cmd/shepherd -acceptance -runs 64` 的统计为：

```text
64/64 通过，参照时间线 64/64 一致
恢复时间 min/P50/P95/max 约 1250/2030/3015/3160 秒
DB 峰值约 75.4%，Cache 峰值约 59.8%
```

额外跑 `-runs 200` 也全部通过，最大恢复时间约 3360 秒。数值会随规则或负载模型调整，以你本地实际运行为准。

## 八条业务规则在哪里

| 规则 | 实现位置 | 网页/验收表现 |
| --- | --- | --- |
| 1. 真实就绪，端口通不算 | `internal/shepherd/engine.go` 的 `probing` 阶段；`ServiceSpec.ReadinessKind/ReadinessProbe` | `readiness_probe_start`、`readiness_passed`；时间轴有 ✓ 标记 |
| 2. 电压不稳只起断电不丢数据的服务 | `updatePower`、`startBlockedReason` 中的 `voltageStable && AbruptSafe` 判断 | 电压稳定线、等待原因、🟠 服务标记 |
| 3. 结算前核对未结收购单 | `maybeStartReconciliation`、`completeReconciliation` | 核对汇总、异常单表、人工队列事件 |
| 4. 发电机先顶且只保关键机器 | 场景中 `GeneratorBacked`、`GeneratorKW`；`handleSecondOutage` | 供电区间、发电机/UPS 曲线、功率违规检查 |
| 5. 终端重连配合恢复进度 | `updateTerminals`、`terminalBackoff` | `terminal_retry` / `terminal_connected` 事件和批次 |
| 6. UPS 充到能正常关机前不起非关键服务 | `UPSThresholdPct`、`updatePower`、`startBlockedReason` | UPS 曲线、`ups_ready`、等待原因 |
| 7. 重启环识别并漂移 | `handleUnexpectedRestart`、`createRestartMigration` | `machine_restart`、`machine_quarantine`、`service_relocated` |
| 8. 坏盘和预指定备用机版本校验 | `createDiskMigration`、`updateMigrations` | `machine_disk_failed`、`standby_verified`，详情含 LSN/版本 |

## 代码结构

```text
main.go / cmd/shepherd/   入口；CLI 实现位于 internal/cli
internal/shepherd/        场景生成、确定性 tick 模拟、编排规则、压力模型
internal/reference/       按规则逐步推演的参照实现
internal/acceptance/      固定种子套件、时间线 DeepEqual、业务规则测试
internal/web/             HTTP API 与 embed 进去的原生 JS/CSS/SVG 网页
```

## 模拟模型说明

时间按 5 秒 tick 推进。压力不是随机波动，而是由当前启动中的服务、预热/核对任务、已放流服务、在线终端数量、缓存命中率和错峰准入阈值共同计算。编排器会在数据库压力余量不足时延迟新服务启动、流量放行和终端批次，因此固定种子下结果完全可复现。

前端没有 npm/打包步骤，静态资源通过 Go `embed` 编入二进制。
