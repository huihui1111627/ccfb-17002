package shepherd

import (
	"fmt"
	"math/rand"
)

const FixedSeedBase int64 = 20261127

// GenerateScenario 用固定种子生成一个可复现的机房规模、依赖和故障组合。
func GenerateScenario(variant int, baseSeed int64) Scenario {
	if baseSeed == 0 {
		baseSeed = FixedSeedBase
	}
	seed := baseSeed + int64(variant)*104729
	r := rand.New(rand.NewSource(seed))

	machineN := 12 + variant
	if machineN > 72 {
		machineN = 72
	}

	mainsAt := 60 + r.Intn(24)*TickSeconds
	outageStart := mainsAt + 180 + r.Intn(73)*TickSeconds
	outageEnd := outageStart + (2+r.Intn(5))*30

	sc := Scenario{
		ID:            fmt.Sprintf("fixed-%02d", variant),
		Variant:       variant,
		Seed:          seed,
		Name:          fmt.Sprintf("%d 台机器 / 固定种子 %d", machineN, seed),
		TickSeconds:   TickSeconds,
		MainsAt:       mainsAt,
		OutageStart:   outageStart,
		OutageEnd:     outageEnd,
		GeneratorKW:   18,
		DBQuotaQPS:    80,
		CacheQuotaQPS: 120,
	}

	sc.Machines = buildMachines(r, machineN)
	sc.Services = buildServices(r, sc.Machines)
	siteN := 4 + r.Intn(18)
	sc.Sites, sc.TerminalTotal = buildSites(r, siteN)
	sc.Orders = buildOrders(r, 8+r.Intn(10), siteN)
	return sc
}

func buildMachines(r *rand.Rand, n int) []MachineSpec {
	ms := []MachineSpec{
		{ID: "db-1", Name: "数据库主机 1", Role: "database", PowerKW: 6.5, GeneratorBacked: true, BootSeconds: 45 + r.Intn(8)*5},
		{ID: "db-2", Name: "数据库主机 2", Role: "database", PowerKW: 6.0, GeneratorBacked: true, BootSeconds: 45 + r.Intn(8)*5},
		{ID: "settle-1", Name: "结算专用机", Role: "settlement", PowerKW: 5.0, GeneratorBacked: true, BootSeconds: 50 + r.Intn(8)*5},
		{ID: "cache-1", Name: "缓存主机", Role: "cache", PowerKW: 4.0, BootSeconds: 45 + r.Intn(10)*5},
	}

	regular := make([]string, 0, n-6)
	roleCycle := []string{"core", "acquisition", "worker", "gateway", "app", "worker", "core"}
	for i := 0; i < n-6; i++ {
		id := fmt.Sprintf("m-%02d", i+1)
		role := roleCycle[i%len(roleCycle)]
		boot := 45 + r.Intn(16)*5
		slow := r.Float64() < 0.38
		if slow {
			boot += 60 + r.Intn(13)*5
		}
		ms = append(ms, MachineSpec{
			ID: id, Name: fmt.Sprintf("应用主机 %02d", i+1), Role: role,
			PowerKW: 2.0 + float64(r.Intn(15))/10.0, BootSeconds: boot, Slow: slow,
		})
		regular = append(regular, id)
	}

	// 两类硬件故障分别使用预先指定/可用的备用机。
	diskIdx := r.Intn(len(regular))
	loopIdx := (diskIdx + 1 + r.Intn(len(regular)-1)) % len(regular)
	if loopIdx == diskIdx {
		loopIdx = (diskIdx + 1) % len(regular)
	}
	version := fmt.Sprintf("lsn-%06x-%02x", r.Intn(0xffffff), r.Intn(256))
	for i := range ms {
		if ms[i].ID == regular[diskIdx] {
			ms[i].DiskFailed = true
			ms[i].DiskDetectSeconds = 30
			ms[i].DesignatedStandbyID = "standby-1"
			ms[i].LastSyncVersion = version
			ms[i].BootSeconds = 50
		}
		if ms[i].ID == regular[loopIdx] {
			ms[i].RestartLoop = true
			ms[i].RestartAt = []int{70, 125}
			ms[i].MigrationTargetID = "standby-2"
			ms[i].BootSeconds = 60
		}
	}

	ms = append(ms,
		MachineSpec{ID: "standby-1", Name: "预指定备用机 1", Role: "standby", PowerKW: 3.0, BootSeconds: 55 + r.Intn(8)*5, StandbyDataVersion: version},
		MachineSpec{ID: "standby-2", Name: "弹性备用机 2", Role: "standby", PowerKW: 3.0, BootSeconds: 50 + r.Intn(10)*5},
	)
	return ms
}

func buildServices(r *rand.Rand, machines []MachineSpec) []ServiceSpec {
	regular := make([]string, 0)
	var coreM, acqM, gwM, workerM string
	for _, m := range machines {
		switch m.Role {
		case "core", "app":
			regular = append(regular, m.ID)
			if coreM == "" {
				coreM = m.ID
			}
		case "acquisition":
			regular = append(regular, m.ID)
			if acqM == "" {
				acqM = m.ID
			}
		case "gateway":
			regular = append(regular, m.ID)
			if gwM == "" {
				gwM = m.ID
			}
		case "worker":
			regular = append(regular, m.ID)
			if workerM == "" {
				workerM = m.ID
			}
		}
	}
	if acqM == "" {
		acqM = regular[0]
	}
	if gwM == "" {
		gwM = regular[len(regular)-1]
	}
	if workerM == "" {
		workerM = regular[0]
	}
	if coreM == "" {
		coreM = regular[0]
	}

	svcs := []ServiceSpec{}
	add := func(id, name, kind, machine string, layer int, critical, abrupt bool, probeKind, probe string, deps []string, start, probeSec, warm int) {
		svcs = append(svcs, ServiceSpec{
			ID: id, Name: name, Kind: kind, MachineID: machine, Layer: layer,
			Critical: critical, AbruptSafe: abrupt, ReadinessKind: probeKind, ReadinessProbe: probe,
			Deps: append([]string{}, deps...), StartSeconds: start, ProbeSeconds: probeSec,
			WarmupSeconds: warm, StartupDBQPS: defaultStartupQPS(kind), WarmupDBQPS: defaultWarmupQPS(kind),
		})
	}

	add("db-primary", "PostgreSQL 主库", "database", "db-1", 0, true, true,
		"db_query", "SELECT 1 并提交一次只读事务", nil, 35, 10, 0)
	add("db-replica", "PostgreSQL 只读副本", "database", "db-2", 0, true, true,
		"db_query", "SELECT pg_last_wal_replay_lsn() 且查询可返回", []string{"db-primary"}, 30, 10, 0)
	add("cache-primary", "Redis 缓存", "cache", "cache-1", 0, false, true,
		"cache_rw", "SET readiness-key 后 GET 比对并 DEL", nil, 20, 10, 120)

	add("ledger-core", "台账核心", "core", coreM, 1, false, true,
		"rpc_probe", "调用试算平衡 RPC，借贷结果一致", []string{"db-primary"}, 25+r.Intn(5)*5, 10, 20)
	add("inventory-core", "库存核心", "core", pickOther(regular, coreM, r), 1, false, true,
		"db_query", "完成一次带事务的库存只读查询", []string{"db-primary", "cache-primary"}, 25+r.Intn(6)*5, 10, 30)
	add("pricing-core", "价格与补贴核心", "core", pickOther(regular, coreM, r), 1, false, true,
		"cache_rw", "读取价格缓存并完成一次试算", []string{"db-primary", "cache-primary"}, 20+r.Intn(7)*5, 5+r.Intn(2)*5, 20)
	add("order-core", "收购单核心", "core", pickOther(regular, coreM, r), 2, false, true,
		"rpc_probe", "创建并回滚一张探测收购单", []string{"ledger-core", "inventory-core"}, 30+r.Intn(6)*5, 10, 30)
	add("payment-core", "付款核心", "payment", pickOther(regular, coreM, r), 2, false, true,
		"rpc_probe", "对探测账户完成幂等查询", []string{"ledger-core"}, 25+r.Intn(6)*5, 10, 0)
	add("acquisition-api", "收购录入服务", "acquisition", acqM, 2, false, false,
		"end_to_end", "经本服务提交一张探测单并读回", []string{"order-core", "inventory-core", "pricing-core"}, 30+r.Intn(8)*5, 10, 20)
	add("settlement", "收购结算服务", "settlement", "settle-1", 3, true, false,
		"settlement_probe", "完成一次结算规则试算且账本可读写", []string{"ledger-core", "order-core", "payment-core"}, 35+r.Intn(8)*5, 15, 0)
	svcs[len(svcs)-1].RequiresReconcile = true
	svcs[len(svcs)-1].External = true

	add("terminal-gateway", "收购点终端网关", "gateway", gwM, 3, false, true,
		"end_to_end", "模拟终端完成一次鉴权和路由往返", []string{"acquisition-api", "settlement"}, 25+r.Intn(7)*5, 10, 20)
	svcs[len(svcs)-1].External = true
	add("api-gateway", "中心 API 网关", "gateway", pickOther(regular, gwM, r), 4, false, true,
		"route_probe", "逐路由探测上游 200 且无 503", []string{"terminal-gateway", "order-core"}, 20+r.Intn(6)*5, 10, 20)
	add("web-console", "管理与监控网页", "web", pickOther(regular, gwM, r), 5, false, true,
		"http_probe", "首页及 /ready 返回真实依赖状态", []string{"api-gateway"}, 15+r.Intn(5)*5, 5, 0)

	kindPool := []string{"worker", "report", "sms", "iot-collector", "batch", "app"}
	extraN := len(machines) - 6 + r.Intn(8)
	for i := 0; i < extraN; i++ {
		id := fmt.Sprintf("svc-%02d", i+1)
		kind := kindPool[r.Intn(len(kindPool))]
		layer := 1 + r.Intn(4)
		machine := regular[r.Intn(len(regular))]
		deps := pickDeps(svcs, layer, r)
		abrupt := true
		if (kind == "worker" || kind == "batch" || kind == "iot-collector") && r.Float64() < 0.45 {
			abrupt = false
		}
		probeKind, probe := probeForKind(kind)
		start := 15 + r.Intn(8)*5
		probeSec := 5 + r.Intn(3)*5
		warm := 0
		if r.Float64() < 0.35 {
			warm = 20 + r.Intn(8)*5
		}
		add(id, fmt.Sprintf("%s-%02d", kindLabel(kind), i+1), kind, machine, layer, false, abrupt,
			probeKind, probe, deps, start, probeSec, warm)
		if r.Float64() < 0.24 {
			svcs[len(svcs)-1].TransientFailures = 1
			if r.Float64() < 0.18 {
				svcs[len(svcs)-1].TransientFailures = 2
			}
		}
	}

	// 重启环机器上的服务给足启动时长，确保在首次真正就绪前被迁走，避免依赖状态回退。
	loopMachine := ""
	for _, m := range machines {
		if m.RestartLoop {
			loopMachine = m.ID
		}
	}
	for i := range svcs {
		if svcs[i].MachineID == loopMachine {
			svcs[i].StartSeconds = 55
			svcs[i].ProbeSeconds = 15
			svcs[i].WarmupSeconds = 0
		}
	}
	return svcs
}

func pickOther(ids []string, not string, r *rand.Rand) string {
	if len(ids) == 0 {
		return not
	}
	for i := 0; i < 10; i++ {
		v := ids[r.Intn(len(ids))]
		if v != not {
			return v
		}
	}
	return ids[0]
}

func pickDeps(existing []ServiceSpec, layer int, r *rand.Rand) []string {
	candidates := []string{}
	for _, s := range existing {
		if s.Layer < layer {
			candidates = append(candidates, s.ID)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	r.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	count := 1 + r.Intn(3)
	if count > len(candidates) {
		count = len(candidates)
	}
	return candidates[:count]
}

func probeForKind(kind string) (string, string) {
	switch kind {
	case "cache":
		return "cache_rw", "SET/GET/DEL 探测键一致"
	case "worker", "batch":
		return "queue_ack", "投递探测消息并在超时前 ACK"
	case "gateway":
		return "end_to_end", "经网关访问真实上游并获得预期响应"
	case "report":
		return "db_query", "完成报表 Schema 探测查询"
	case "sms":
		return "rpc_probe", "向运营商通道发送幂等心跳探测"
	case "iot-collector":
		return "mqtt_roundtrip", "建立订阅并收到自己的探测报文"
	default:
		return "rpc_probe", "调用版本化健康 RPC 并检查依赖句柄"
	}
}

func kindLabel(kind string) string {
	labels := map[string]string{
		"worker": "异步任务", "report": "报表", "sms": "通知",
		"iot-collector": "终端采集", "batch": "批处理", "app": "业务应用",
	}
	if v, ok := labels[kind]; ok {
		return v
	}
	return kind
}

func defaultStartupQPS(kind string) float64 {
	switch kind {
	case "database":
		return 4.2
	case "cache":
		return 0.9
	case "batch", "report":
		return 3.0
	default:
		return 2.4
	}
}

func defaultWarmupQPS(kind string) float64 {
	if kind == "cache" {
		return 8.5
	}
	return 2.5
}

func buildSites(r *rand.Rand, n int) ([]TerminalSite, int) {
	names := []string{"一号收购点", "二号收购点", "河东收购站", "西山牧业点", "河滩合作社", "北塬收购点", "红旗苏木站", "雪原村点"}
	sites := make([]TerminalSite, 0, n)
	total := 0
	for i := 0; i < n; i++ {
		count := 6 + r.Intn(35)
		total += count
		name := fmt.Sprintf("%s %02d", names[i%len(names)], i/len(names)+1)
		if i < len(names) {
			name = names[i]
		}
		sites = append(sites, TerminalSite{ID: fmt.Sprintf("site-%02d", i+1), Name: name, Terminals: count})
	}
	return sites, total
}

func buildOrders(r *rand.Rand, n, siteN int) []PendingOrder {
	items := []string{"活羊", "羊毛", "饲草", "玉米", "牛粪有机肥", "奶制品"}
	orders := make([]PendingOrder, 0, n)
	mismatchN := 1 + r.Intn(3)
	for i := 0; i < n; i++ {
		expected := 800 + r.Intn(4200)
		mismatch := i < mismatchN
		actual := expected
		reason := ""
		if mismatch {
			delta := 30 + r.Intn(260)
			if r.Intn(2) == 0 {
				actual = expected - delta
				reason = fmt.Sprintf("地磅上传 %dkg，中心入库 %dkg，差 %dkg", expected, actual, delta)
			} else {
				actual = expected + delta
				reason = fmt.Sprintf("收购单金额与付款流水差 ¥%d，需人工核对", delta*7)
			}
		}
		siteIdx := r.Intn(siteN)
		orders = append(orders, PendingOrder{
			ID: fmt.Sprintf("CG-BEFORE-OUTAGE-%03d", i+1), SiteID: fmt.Sprintf("site-%02d", siteIdx+1),
			SiteName: fmt.Sprintf("收购点 %02d", siteIdx+1), Item: items[r.Intn(len(items))],
			Expected: expected, Actual: actual, Mismatch: mismatch, Reason: reason,
		})
	}
	return orders
}
