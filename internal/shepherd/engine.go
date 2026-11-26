package shepherd

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
)

type machineState struct {
	timing     MachineTiming
	spec       MachineSpec
	status     string // reserved|pending|booting|up|disk_failed|quarantined|powered_off|stopping
	baseStart  int
	deadline   int
	restarts   int
	restartIdx int
}

type serviceState struct {
	timing      ServiceTiming
	spec        ServiceSpec
	status      string // pending|backoff|starting|probing|warming|reconciling|ready|open
	nextAction  int
	stageStart  int
	failureLeft int
	portEmitted bool
	waitReason  string
	waitSince   int
	trafficPct  float64
}

type siteState struct {
	spec      TerminalSite
	connected int
	active    float64
	attempts  int
	nextCheck int
}

type migrationState struct {
	fromID        string
	toID          string
	reason        string
	verifyData    bool
	verifyUntil   int
	verifyStarted bool
	done          bool
	serviceIDs    []string
}

type Engine struct {
	sc Scenario

	machines map[string]*machineState
	services map[string]*serviceState
	sites    map[string]*siteState
	order    []string

	items   []Item
	samples []Sample
	waits   []WaitInterval

	migrations map[string]*migrationState

	mains         bool
	mainsSince    int
	voltageStable bool
	upsCharge     float64
	upsReady      bool

	nextMachineSlot int
	nextServiceSlot int
	nextTrafficSlot int

	reconcileStartedAt *int
	reconcileCompleted *int
	reconcileReport    ReconcileReport

	completeAt int
	endAt      int
	now        int
}

func Run(sc Scenario) *Report {
	e := newEngine(sc)
	return e.run()
}

func newEngine(sc Scenario) *Engine {
	e := &Engine{
		sc:         sc,
		machines:   map[string]*machineState{},
		services:   map[string]*serviceState{},
		sites:      map[string]*siteState{},
		migrations: map[string]*migrationState{},
		mains:      false,
		mainsSince: -1,
		upsCharge:  0,
	}
	for _, m := range sc.Machines {
		status := "pending"
		if m.Role == "standby" {
			status = "reserved"
		}
		e.machines[m.ID] = &machineState{
			spec:   m,
			status: status,
			timing: MachineTiming{ID: m.ID, Name: m.Name, Role: m.Role, FinalMachineID: m.ID},
		}
	}
	for _, s := range sc.Services {
		ss := &serviceState{
			spec:        s,
			status:      "pending",
			failureLeft: s.TransientFailures,
			timing: ServiceTiming{
				ID: s.ID, Name: s.Name, Kind: s.Kind, Layer: s.Layer,
				Critical: s.Critical, AbruptSafe: s.AbruptSafe,
				OriginalMachine: s.MachineID, MachineID: s.MachineID,
				Status: "pending", TransientLeft: s.TransientFailures,
			},
		}
		e.services[s.ID] = ss
		e.order = append(e.order, s.ID)
	}
	sort.Strings(e.order)
	for _, site := range sc.Sites {
		e.sites[site.ID] = &siteState{spec: site, nextCheck: sc.MainsAt}
	}
	e.reconcileReport = ReconcileReport{TotalOrders: len(sc.Orders), Mismatches: []PendingOrder{}}
	return e
}

func (e *Engine) run() *Report {
	e.add(Item{T: 0, Kind: "grid_outage", Subject: "grid", Label: "市电中断", Detail: "UPS 已放空；柴油发电机自动启动"})
	e.add(Item{T: 0, Kind: "generator_start", Subject: "generator", Label: "柴油发电机先行供电",
		Detail: fmt.Sprintf("带载上限 %.0f kW，仅保障数据库与结算主机", e.sc.GeneratorKW)})

	// 关键发电机后备机器在 t=0 立即有序上电。
	for _, id := range e.sortedMachineIDs() {
		m := e.machines[id]
		if m.spec.GeneratorBacked {
			e.startMachineBoot(m, 0)
		}
	}

	for now := 0; now <= MaxSimSeconds; now += TickSeconds {
		e.now = now
		e.updatePower(now)
		e.updateMigrations(now)
		e.updateMachines(now)
		e.updateMigrations(now)
		e.updateServices(now)
		e.updateTerminals(now)
		e.samples = append(e.samples, e.sample(now))

		if e.allComplete() && e.completeAt == 0 {
			e.completeAt = now
		}
		// 恢复完成后再观察 120s，让终端流量爬坡到稳态，验证压力不会滞后越限。
		if e.completeAt > 0 && now >= e.completeAt+120 {
			e.endAt = now
			break
		}
	}
	return e.buildReport()
}

func (e *Engine) add(it Item) {
	if it.Label == "" {
		it.Label = it.Kind
	}
	e.items = append(e.items, it)
}

func (e *Engine) sortedMachineIDs() []string {
	ids := make([]string, 0, len(e.machines))
	for id := range e.machines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (e *Engine) sortedServiceIDsByPriority() []string {
	ids := append([]string{}, e.order...)
	sort.Slice(ids, func(i, j int) bool {
		a, b := e.services[ids[i]], e.services[ids[j]]
		if a.spec.Layer != b.spec.Layer {
			return a.spec.Layer < b.spec.Layer
		}
		if a.spec.Critical != b.spec.Critical {
			return a.spec.Critical // true first
		}
		if a.spec.Kind != b.spec.Kind {
			return a.spec.Kind < b.spec.Kind
		}
		return a.spec.ID < b.spec.ID
	})
	return ids
}

func (e *Engine) startMachineBoot(m *machineState, now int) {
	m.status = "booting"
	m.timing.BootStartAt = intPtr(now)
	m.baseStart = now
	m.deadline = 0
	e.add(Item{T: now, Kind: "machine_boot_start", Subject: m.spec.ID, Label: "机器开始开机",
		Detail: fmt.Sprintf("%s · 预计 POST/引导 %ds", m.spec.Name, m.spec.BootSeconds)})
}

func (e *Engine) requestStandby(id string) {
	if m, ok := e.machines[id]; ok && m.status == "reserved" {
		m.status = "pending"
	}
}

func (e *Engine) anyMachineStopping() bool {
	for _, m := range e.machines {
		if m.status == "stopping" {
			return true
		}
	}
	return false
}

// handleSecondOutage 处理恢复途中的再次停电：发电机只保关键机器，
// 其余机器借助已充到门槛的 UPS 正常关机；若 UPS 未就绪，规则上它们本来就尚未启动。
func (e *Engine) handleSecondOutage(now int) {
	for _, id := range e.sortedMachineIDs() {
		m := e.machines[id]
		if m.spec.GeneratorBacked {
			continue
		}
		if m.status != "up" && m.status != "booting" {
			continue
		}
		m.status = "stopping"
		m.deadline = now + ShutdownSeconds
		e.add(Item{T: now, Kind: "machine_graceful_stop", Subject: id, Label: "再次停电，UPS 支撑优雅关机",
			Detail: fmt.Sprintf("发电机容量只保数据库/结算；该机在 %ds 内停止服务并下电", ShutdownSeconds)})
		e.interruptServicesOnMachine(id, now,
			"恢复中途再次停电；非关键负载由 UPS 执行正常关机，复电后重新走真实就绪检查",
			"service_graceful_stop", "服务随 UPS 正常关机")
	}

	dropped := 0
	for id, st := range e.sites {
		dropped += st.connected
		st.connected = 0
		st.active = 0
		st.attempts = 0
		st.nextCheck = e.sc.OutageEnd + stableJitter(id, 17, 6)*TickSeconds
	}
	// 复电后备用机上的数据校验窗口重新计时，避免停电把 20s 核对过程“跳”过去。
	for _, mig := range e.migrations {
		if !mig.done && mig.verifyData {
			mig.verifyStarted = false
			mig.verifyUntil = 0
		}
	}
	if dropped > 0 {
		e.add(Item{T: now, Kind: "terminals_disconnected", Subject: "terminals", Label: "终端连接因再次停电全部断开",
			Detail: fmt.Sprintf("已在线 %d 台清零；复电后由网关按 Retry-After 和压力批次重新接入", dropped)})
	}
}

func (e *Engine) updatePower(now int) {
	shouldMains := now >= e.sc.MainsAt
	if now >= e.sc.OutageStart && now < e.sc.OutageEnd {
		shouldMains = false
	}

	if now == e.sc.MainsAt {
		e.mains = true
		e.mainsSince = now
		e.add(Item{T: now, Kind: "mains_restored", Subject: "grid", Label: "市电恢复",
			Detail: "发电机退出主供电，负载切回市电；UPS 开始充电"})
		e.add(Item{T: now, Kind: "power_transfer", Subject: "generator", Label: "发电→市电切换完成", Detail: "关键数据库/结算负载未中断"})
	}

	if now == e.sc.OutageStart {
		e.mains = false
		e.mainsSince = -1
		e.voltageStable = false
		e.add(Item{T: now, Kind: "mains_lost", Subject: "grid", Label: "恢复中途再次停电",
			Detail: "发电机自启动，数据库与结算主机继续受保障"})
		e.add(Item{T: now, Kind: "generator_start", Subject: "generator", Label: "发电机重新带载", Detail: "非关键负载由 UPS 完成正常关机，发电机只保留数据库/结算"})
		e.handleSecondOutage(now)
	}

	if now == e.sc.OutageEnd {
		e.mains = true
		e.mainsSince = now
		e.add(Item{T: now, Kind: "mains_restored", Subject: "grid", Label: "市电再次恢复",
			Detail: "重新计算十分钟电压观察窗，UPS 继续充电"})
		e.add(Item{T: now, Kind: "power_transfer", Subject: "generator", Label: "再次切回市电", Detail: "发电机进入热备"})
	}

	// 兜底：若事件边界由其他方式构造，仍保持状态正确。
	if now != e.sc.MainsAt && now != e.sc.OutageEnd && now != e.sc.OutageStart {
		e.mains = shouldMains
		if shouldMains && e.mainsSince < 0 {
			e.mainsSince = now
		}
		if !shouldMains {
			e.mainsSince = -1
		}
	}

	upsWasReady := e.upsReady
	if e.mains {
		e.upsCharge = math.Min(100, e.upsCharge+UPSChargePerSec*TickSeconds)
	} else if e.anyMachineStopping() {
		e.upsCharge = math.Max(0, e.upsCharge-UPSDrainPerSec*TickSeconds)
	}
	e.upsReady = e.upsCharge >= UPSThresholdPct
	if !upsWasReady && e.upsReady {
		e.add(Item{T: now, Kind: "ups_ready", Subject: "ups", Label: "UPS 充电至可安全关机",
			Detail: fmt.Sprintf("电量 %.0f%% ≥ %.0f%%，允许/恢复非关键服务启动", e.upsCharge, UPSThresholdPct)})
	}

	stableAt := -1
	if e.mainsSince >= 0 {
		stableAt = e.mainsSince + VoltageWindow
	}
	if !e.voltageStable && stableAt >= 0 && now >= stableAt {
		e.voltageStable = true
		e.add(Item{T: now, Kind: "voltage_stable", Subject: "grid", Label: "电压观察窗结束",
			Detail: "后续断电不会再造成启动期未落盘数据风险，允许有未落盘数据的服务启动"})
	}
}

func (e *Engine) updateMachines(now int) {
	// 注入的磁盘损坏和重启环优先在已开始引导的机器上检测。
	for _, id := range e.sortedMachineIDs() {
		m := e.machines[id]

		if m.status == "stopping" && now >= m.deadline {
			m.status = "powered_off"
			e.add(Item{T: now, Kind: "machine_power_off", Subject: id, Label: "UPS 正常关机完成",
				Detail: "机器已安全下电；市电恢复并重新满足 UPS 电量门槛后再错峰开机"})
			continue
		}

		if m.status == "booting" && m.spec.DiskFailed && now >= m.baseStart+m.spec.DiskDetectSeconds {
			m.status = "disk_failed"
			t := now
			m.timing.DiskFailedAt = &t
			e.add(Item{T: now, Kind: "machine_disk_failed", Subject: id, Label: "硬盘损坏，无法引导",
				Detail: fmt.Sprintf("POST 报告系统/数据盘故障；将切换到预指定备用机 %s", m.spec.DesignatedStandbyID)})
			e.createDiskMigration(m, now)
			continue
		}

		if (m.status == "booting" || m.status == "up") && m.spec.RestartLoop && m.restartIdx < len(m.spec.RestartAt) {
			// RestartAt 是相对首次引导开始的绝对偏移；baseStart 在意外重启时不覆盖。
			fireAt := m.baseStart + m.spec.RestartAt[m.restartIdx]
			if now >= fireAt {
				e.handleUnexpectedRestart(m, now)
				continue
			}
		}

		if m.status == "booting" && now >= *m.timing.BootStartAt+m.spec.BootSeconds {
			m.status = "up"
			t := now
			m.timing.BootedAt = &t
			e.add(Item{T: now, Kind: "machine_booted", Subject: id, Label: "机器引导完成", Detail: "操作系统和运维代理已就绪"})
		}
	}

	// 错峰开机：最多 4 台同时引导，相邻上电至少间隔 10 秒。
	booting := 0
	for _, m := range e.machines {
		if m.status == "booting" {
			booting++
		}
	}
	for _, id := range e.sortedMachineIDs() {
		m := e.machines[id]
		if m.status != "pending" && m.status != "powered_off" {
			continue
		}
		if !e.mains || !e.upsReady {
			continue
		}
		if booting >= 4 || now < e.nextMachineSlot {
			continue
		}
		e.startMachineBoot(m, now)
		e.nextMachineSlot = now + 2*TickSeconds
		booting++
	}
}

func (e *Engine) handleUnexpectedRestart(m *machineState, now int) {
	m.restartIdx++
	m.restarts++
	e.add(Item{T: now, Kind: "machine_restart", Subject: m.spec.ID, Label: fmt.Sprintf("第 %d 次意外重启", m.restarts),
		Detail: "运维代理记录到非正常复位；该机上的启动探测全部作废"})
	e.resetServicesOnMachine(m.spec.ID, now, "机器意外重启，启动状态作废")

	if m.restarts >= 2 {
		m.status = "quarantined"
		t := now
		m.timing.QuarantinedAt = &t
		e.add(Item{T: now, Kind: "machine_quarantine", Subject: m.spec.ID, Label: "识别为反复重启并隔离",
			Detail: fmt.Sprintf("短时间内 %d 次非正常重启；服务漂移到 %s", m.restarts, m.spec.MigrationTargetID)})
		e.createRestartMigration(m, now)
		return
	}

	boot := now
	m.status = "booting"
	m.timing.BootStartAt = &boot
	m.timing.BootedAt = nil
}

func (e *Engine) resetServicesOnMachine(machineID string, now int, reason string) {
	e.interruptServicesOnMachine(machineID, now, reason,
		"service_start_interrupted", "启动被机器故障打断")
}

func (e *Engine) interruptServicesOnMachine(machineID string, now int, reason, eventKind, eventLabel string) {
	for _, id := range e.order {
		s := e.services[id]
		if s.timing.MachineID != machineID {
			continue
		}
		if s.status != "pending" && s.status != "backoff" {
			e.add(Item{T: now, Kind: eventKind, Subject: id, Label: eventLabel, Detail: reason})
		}
		s.status = "pending"
		s.nextAction = now + TickSeconds
		s.stageStart = 0
		s.trafficPct = 0
		s.portEmitted = false
		s.timing.PortOpenAt = nil
		// FirstReadyAt 保留为历史；当前 ReadyAt 必须清空，恢复时重新执行真实就绪检查。
		s.timing.ReadyAt = nil
		s.timing.WarmedAt = nil
		s.timing.TrafficAt = nil
		s.timing.Status = "pending"
		e.setWait(s, reason)
	}
}

func (e *Engine) createDiskMigration(m *machineState, now int) {
	target := m.spec.DesignatedStandbyID
	e.requestStandby(target)
	e.migrations[m.spec.ID] = &migrationState{
		fromID: m.spec.ID, toID: target, verifyData: true, reason: "disk_failure",
		serviceIDs: e.servicesOnMachine(m.spec.ID),
	}
	for _, sid := range e.migrations[m.spec.ID].serviceIDs {
		s := e.services[sid]
		s.status = "pending"
		s.nextAction = now + TickSeconds
		e.setWait(s, "硬盘损坏；等待预指定备用机启动并核对停电前最后同步版本")
	}
}

func (e *Engine) createRestartMigration(m *machineState, now int) {
	target := m.spec.MigrationTargetID
	e.requestStandby(target)
	e.migrations[m.spec.ID] = &migrationState{
		fromID: m.spec.ID, toID: target, verifyData: false, reason: "restart_loop",
		serviceIDs: e.servicesOnMachine(m.spec.ID),
	}
	for _, sid := range e.migrations[m.spec.ID].serviceIDs {
		s := e.services[sid]
		s.status = "pending"
		s.nextAction = now + TickSeconds
		e.setWait(s, "原机反复重启已隔离；等待漂移到其他机器")
	}
}

func (e *Engine) servicesOnMachine(machineID string) []string {
	ids := []string{}
	for _, id := range e.order {
		if e.services[id].spec.MachineID == machineID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (e *Engine) updateMigrations(now int) {
	keys := make([]string, 0, len(e.migrations))
	for k := range e.migrations {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, from := range keys {
		mig := e.migrations[from]
		if mig.done {
			continue
		}
		target, ok := e.machines[mig.toID]
		if !ok {
			continue
		}
		if target.status == "reserved" {
			target.status = "pending"
		}
		if target.status == "pending" && e.mains && e.upsReady && now >= e.nextMachineSlot {
			e.startMachineBoot(target, now)
			e.nextMachineSlot = now + 2*TickSeconds
			continue
		}
		if target.status != "up" {
			continue
		}

		if mig.verifyData {
			if !mig.verifyStarted {
				mig.verifyStarted = true
				mig.verifyUntil = now + 4*TickSeconds
				e.add(Item{T: now, Kind: "standby_verify_start", Subject: mig.toID, Label: "核对备用机数据版本",
					Detail: fmt.Sprintf("必须等于故障机停电前最后同步版本 %s", e.machines[from].spec.LastSyncVersion)})
			}
			if now < mig.verifyUntil {
				continue
			}
			fromSpec := e.machines[from].spec
			if target.spec.StandbyDataVersion != fromSpec.LastSyncVersion {
				e.add(Item{T: now, Kind: "standby_verify_failed", Subject: mig.toID, Label: "备用数据版本不一致，禁止起服务",
					Detail: fmt.Sprintf("备用=%s，最后同步=%s", target.spec.StandbyDataVersion, fromSpec.LastSyncVersion)})
				continue
			}
			e.add(Item{T: now, Kind: "standby_verified", Subject: mig.toID, Label: "备用数据版本核对通过",
				Detail: fmt.Sprintf("数据版本 %s 为停电前最后一次同步点", target.spec.StandbyDataVersion)})
		}

		for _, sid := range mig.serviceIDs {
			s := e.services[sid]
			s.timing.MachineID = mig.toID
			s.spec.MachineID = mig.toID
			s.status = "pending"
			s.nextAction = now + TickSeconds
			s.failureLeft = s.spec.TransientFailures
			e.add(Item{T: now, Kind: "service_relocated", Subject: sid, Label: "服务漂移到备用机器",
				Detail: fmt.Sprintf("%s → %s；%s", from, mig.toID, map[bool]string{true: "数据版本已确认", false: "目标机资源充足"}[mig.verifyData])})
		}
		mig.done = true
	}
}

func (e *Engine) updateServices(now int) {
	// 先推进正在执行的生命周期。
	for _, id := range e.sortedServiceIDsByPriority() {
		s := e.services[id]
		switch s.status {
		case "starting":
			if s.timing.PortOpenAt == nil && now >= s.stageStart+2*TickSeconds {
				t := now
				s.timing.PortOpenAt = &t
				e.add(Item{T: now, Kind: "port_open", Subject: id, Label: "端口已监听（不算就绪）",
					Detail: "编排器继续执行服务声明的真实读写/探测检查"})
			}
			if now >= s.nextAction {
				if s.failureLeft > 0 {
					s.failureLeft--
					s.timing.TransientLeft = s.failureLeft
					s.status = "backoff"
					backoff := 15
					s.nextAction = now + backoff
					e.add(Item{T: now, Kind: "service_start_failed", Subject: id, Label: "启动/迁移失败，将重试",
						Detail: fmt.Sprintf("注入的瞬时失败剩余 %d 次；%ds 后指数退避重试", s.failureLeft, backoff)})
					e.setWait(s, fmt.Sprintf("启动失败，%ds 退避后重试", backoff))
				} else {
					s.status = "probing"
					s.nextAction = now + s.spec.ProbeSeconds
					e.add(Item{T: now, Kind: "readiness_probe_start", Subject: id, Label: "执行真实就绪检查",
						Detail: fmt.Sprintf("[%s] %s", s.spec.ReadinessKind, s.spec.ReadinessProbe)})
				}
			}
		case "probing":
			if now >= s.nextAction {
				t := now
				if s.timing.FirstReadyAt == nil {
					s.timing.FirstReadyAt = &t
				}
				s.timing.ReadyAt = &t
				s.timing.Status = "ready"
				s.status = "ready"
				e.clearWait(s)
				e.add(Item{T: now, Kind: "readiness_passed", Subject: id, Label: "通过真实就绪检查",
					Detail: readinessDetail(s.spec)})
				if s.spec.WarmupSeconds > 0 {
					s.status = "warming"
					s.stageStart = now
					s.nextAction = now + s.spec.WarmupSeconds
					e.add(Item{T: now, Kind: "warmup_start", Subject: id, Label: "开始受控预热", Detail: "分批加载热点数据，避免缓存空窗直接打爆数据库"})
				}
			}
		case "warming":
			if now >= s.nextAction {
				t := now
				s.timing.WarmedAt = &t
				s.status = "ready"
				e.add(Item{T: now, Kind: "warmup_done", Subject: id, Label: "预热完成", Detail: "热点数据已载入，允许放行后续流量"})
			}
		case "reconciling":
			if now >= s.nextAction {
				e.completeReconciliation(now)
				s.status = "ready"
				t := now
				s.timing.ReconciledAt = &t
			}
		case "open":
			s.trafficPct = math.Min(1, s.trafficPct+0.25)
		}
	}

	activeStarts := 0
	for _, s := range e.services {
		if s.status == "starting" || s.status == "probing" {
			activeStarts++
		}
	}
	maxStarts := 2 + e.sc.Variant/10
	if maxStarts > 7 {
		maxStarts = 7
	}

	// 再按依赖层级和压力余量放行新启动。
	for _, id := range e.sortedServiceIDsByPriority() {
		s := e.services[id]
		if s.status != "pending" && s.status != "backoff" && s.status != "ready" {
			continue
		}
		if s.timing.TrafficAt != nil {
			continue
		}

		// ready 状态在这里只负责放行流量/核对，不重复启动。
		if s.status == "ready" {
			e.maybeStartReconciliation(s, now)
			e.maybeOpenTraffic(s, now)
			continue
		}

		if now < s.nextAction {
			continue
		}
		if reason := e.startBlockedReason(s, now); reason != "" {
			e.setWait(s, reason)
			continue
		}
		if activeStarts >= maxStarts || now < e.nextServiceSlot {
			e.setWait(s, fmt.Sprintf("错峰启动槽位（并发 %d/%d），避免连接风暴", activeStarts, maxStarts))
			continue
		}
		dbQPS, _, _, _ := e.pressureParts(now)
		projected := (dbQPS + s.spec.StartupDBQPS + 0.8) / e.sc.DBQuotaQPS * 100
		if projected >= PressureSoftLimit+4 {
			e.setWait(s, fmt.Sprintf("数据库压力余量不足（预测 %.1f%%），等待错峰", projected))
			continue
		}

		e.clearWait(s)
		attemptAt := now
		if s.timing.FirstStartAt == nil {
			s.timing.FirstStartAt = &attemptAt
		}
		s.timing.LastStartAt = &attemptAt
		s.timing.Attempts++
		s.status = "starting"
		s.stageStart = now
		s.nextAction = now + s.spec.StartSeconds
		s.portEmitted = false
		s.timing.Status = "starting"
		activeStarts++
		e.nextServiceSlot = now + 2*TickSeconds
		e.add(Item{T: now, Kind: "service_start", Subject: id, Label: "服务开始启动",
			Detail: fmt.Sprintf("第 %d 次尝试 · 机器 %s · 声明就绪检查：%s", s.timing.Attempts, s.timing.MachineID, s.spec.ReadinessKind)})
	}
}

func readinessDetail(sp ServiceSpec) string {
	switch sp.ReadinessKind {
	case "db_query":
		return "真实查询成功返回，而非仅 TCP 端口可连"
	case "cache_rw":
		return "写入键可读出且值一致，缓存读写链路正常"
	case "queue_ack":
		return "探测消息已被本实例消费并 ACK"
	case "end_to_end":
		return "完整依赖往返探测通过"
	default:
		return sp.ReadinessProbe + "；探测通过"
	}
}

func (e *Engine) startBlockedReason(s *serviceState, now int) string {
	m := e.machines[s.timing.MachineID]
	if m == nil {
		return "尚未分配机器"
	}
	if _, migrating := e.migrations[m.spec.ID]; migrating && !e.migrations[m.spec.ID].done {
		if e.migrations[m.spec.ID].verifyData && e.migrations[m.spec.ID].verifyStarted {
			return "备用机正在核对停电前最后同步数据版本"
		}
		return "等待故障机器处置和服务漂移"
	}
	switch m.status {
	case "disk_failed":
		return "机器硬盘损坏，等待切换预指定备用机并核对数据版本"
	case "quarantined":
		return "原机反复重启已隔离，等待服务漂移"
	case "reserved":
		return "备用机尚未被故障切换流程唤醒"
	case "pending":
		if !e.upsReady {
			return fmt.Sprintf("等待 UPS 充至可安全关机（当前 %.0f%%/80%%）", e.upsCharge)
		}
		if !e.mains {
			return "市电未恢复，非关键机器等待供电"
		}
		return "等待机器错峰开机"
	case "booting":
		return fmt.Sprintf("等待机器 %s 完成引导", m.spec.ID)
	case "powered_off":
		return "机器在再次停电后已安全关机，等待供电恢复并重新开机"
	}
	if m.status != "up" {
		return "等待机器开机完成"
	}
	if !s.spec.Critical && !e.upsReady {
		return fmt.Sprintf("UPS 尚未达到可安全撑过正常关机的电量（%.0f%%/80%%），非关键服务不启动", e.upsCharge)
	}
	if !e.voltageStable && !s.spec.AbruptSafe {
		return "来电后十分钟电压未稳；本服务有未落盘数据，需等电压稳定"
	}
	for _, depID := range s.spec.Deps {
		dep := e.services[depID]
		if dep == nil || dep.timing.ReadyAt == nil {
			what := "依赖服务"
			if dep != nil {
				what = dep.spec.Name
			}
			return fmt.Sprintf("等待依赖 %s 通过真实就绪检查", what)
		}
		if *dep.timing.ReadyAt+TickSeconds > now {
			return fmt.Sprintf("依赖 %s 刚就绪，留出一个检查周期", dep.spec.Name)
		}
	}
	if now < s.nextAction {
		return fmt.Sprintf("失败退避中，%ds 后重试", s.nextAction-now)
	}
	return ""
}

func (e *Engine) maybeStartReconciliation(s *serviceState, now int) {
	if !s.spec.RequiresReconcile || e.reconcileStartedAt != nil {
		return
	}
	t := now
	e.reconcileStartedAt = &t
	s.status = "reconciling"
	duration := 4*TickSeconds + len(e.sc.Orders)*2
	// 对齐到 tick。
	duration = ((duration + TickSeconds - 1) / TickSeconds) * TickSeconds
	s.nextAction = now + duration
	e.add(Item{T: now, Kind: "reconcile_start", Subject: s.spec.ID, Label: "核对停电前未结收购单",
		Detail: fmt.Sprintf("逐单核对 %d 批重量、金额和付款流水；对不上的转人工", len(e.sc.Orders))})
}

func (e *Engine) completeReconciliation(now int) {
	matched := 0
	mismatches := []PendingOrder{}
	for _, o := range e.sc.Orders {
		if o.Mismatch {
			mismatches = append(mismatches, o)
		} else {
			matched++
		}
	}
	t := now
	e.reconcileCompleted = &t
	e.reconcileReport.StartedAt = e.reconcileStartedAt
	e.reconcileReport.CompletedAt = &t
	e.reconcileReport.TotalOrders = len(e.sc.Orders)
	e.reconcileReport.MatchedOrders = matched
	e.reconcileReport.MismatchOrders = len(mismatches)
	e.reconcileReport.Mismatches = mismatches
	e.reconcileReport.HandedToManual = len(mismatches) > 0
	e.add(Item{T: now, Kind: "reconcile_done", Subject: "settlement", Label: "停电前收购单核对完成",
		Detail: fmt.Sprintf("一致 %d 单，异常 %d 单已进入人工处理队列；异常单不阻塞其他收购业务", matched, len(mismatches))})
	for _, o := range mismatches {
		e.add(Item{T: now, Kind: "reconcile_mismatch", Subject: o.ID, Label: "异常单标记并交人工",
			Detail: fmt.Sprintf("%s（%s/%s）：%s", o.ID, o.SiteName, o.Item, o.Reason)})
	}
	e.add(Item{T: now, Kind: "manual_queue", Subject: "manual-console", Label: "人工处理队列已生成",
		Detail: fmt.Sprintf("%d 条重量/金额差异等待收购点和财务复核", len(mismatches))})
}

func (e *Engine) maybeOpenTraffic(s *serviceState, now int) {
	if s.timing.TrafficAt != nil || s.status != "ready" {
		return
	}
	if s.spec.RequiresReconcile && e.reconcileCompleted == nil {
		return
	}
	for _, depID := range s.spec.Deps {
		dep := e.services[depID]
		if dep == nil || dep.timing.TrafficAt == nil || *dep.timing.TrafficAt+TickSeconds > now {
			return
		}
	}
	if now < e.nextTrafficSlot {
		e.setWait(s, "流量放行错峰窗口，等待前一波灰度指标")
		return
	}
	dbQPS, _, _, _ := e.pressureParts(now)
	projected := dbQPS / e.sc.DBQuotaQPS * 100
	if projected >= PressureSoftLimit-6 {
		e.setWait(s, fmt.Sprintf("数据库压力 %.1f%% 偏高，暂缓放流", projected))
		return
	}
	t := now
	s.timing.TrafficAt = &t
	s.timing.Status = "open"
	s.status = "open"
	s.trafficPct = 0.25
	e.clearWait(s)
	e.nextTrafficSlot = now + 2*TickSeconds
	e.add(Item{T: now, Kind: "traffic_open", Subject: s.spec.ID, Label: "开始接流量",
		Detail: "先放入 25% 灰度流量，随后在 60s 内平滑放大；数据库压力超阈值时继续暂缓后续波次"})
}

func (e *Engine) updateTerminals(now int) {
	ids := make([]string, 0, len(e.sites))
	for id := range e.sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	gateway := e.services["terminal-gateway"]
	settlement := e.services["settlement"]
	serverReady := gateway != nil && gateway.timing.TrafficAt != nil && settlement != nil && settlement.timing.TrafficAt != nil

	for _, id := range ids {
		st := e.sites[id]
		if st.connected > 0 {
			st.active += (float64(st.connected) - st.active) * 0.09
			if st.active > float64(st.connected) {
				st.active = float64(st.connected)
			}
		}
		if now < st.nextCheck {
			continue
		}
		remaining := st.spec.Terminals - st.connected
		if remaining <= 0 {
			continue
		}

		if !serverReady {
			st.attempts++
			retry := terminalBackoff(st.attempts, st.spec.ID)
			st.nextCheck = now + retry
			reason := "终端网关尚未放行真实流量"
			if gateway != nil && gateway.waitReason != "" {
				reason = gateway.waitReason
			} else if settlement != nil && settlement.timing.TrafficAt == nil {
				if e.reconcileCompleted == nil && e.reconcileStartedAt != nil {
					reason = "结算服务正在核对停电前未结收购单"
				} else {
					reason = "结算与收购链路尚未完成恢复/核对"
				}
			}
			e.add(Item{T: now, Kind: "terminal_retry", Subject: id, Label: "终端齐连被服务端引导稍后重试",
				Detail: fmt.Sprintf("%d 台终端收到 503 + Retry-After=%ds；原因：%s", remaining, retry, reason)})
			continue
		}

		dbQPS, _, _, _ := e.pressureParts(now)
		if dbQPS/e.sc.DBQuotaQPS*100 >= PressureSoftLimit-2 {
			st.attempts++
			retry := 10 + stableJitter(id, st.attempts, 3)*5
			st.nextCheck = now + retry
			e.add(Item{T: now, Kind: "terminal_retry", Subject: id, Label: "服务端按压力要求终端退避",
				Detail: fmt.Sprintf("数据库压力 %.1f%%，%d 台终端收到 Retry-After=%ds", dbQPS/e.sc.DBQuotaQPS*100, remaining, retry)})
			continue
		}

		cohort := 8 + stableJitter(id, st.attempts, 4)*4
		if cohort > remaining {
			cohort = remaining
		}
		// 每个 tick 全局最多新接入 48 台，保持平滑。
		if cohort > 48 {
			cohort = 48
		}
		st.connected += cohort
		st.attempts++
		if st.connected < st.spec.Terminals {
			st.nextCheck = now + 2*TickSeconds
		} else {
			st.nextCheck = MaxSimSeconds + TickSeconds
		}
		e.add(Item{T: now, Kind: "terminal_connected", Subject: id, Label: "终端批次已协调重连",
			Detail: fmt.Sprintf("本批 %d 台，点内已连 %d/%d；终端按抖动错峰，不再齐打数据库", cohort, st.connected, st.spec.Terminals)})
	}
}

func terminalBackoff(attempt int, id string) int {
	base := 15
	if attempt >= 2 {
		base = 30
	}
	if attempt >= 4 {
		base = 60
	}
	jitter := stableJitter(id, attempt, 4) * 5 // 0/5/10/15
	return base + jitter
}

func stableJitter(id string, salt, mod int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s#%d", id, salt)))
	return int(h.Sum32() % uint32(mod))
}

func (e *Engine) pressureParts(now int) (dbQPS, cacheQPS, hitRate float64, activeTerminals float64) {
	if cache := e.services["cache-primary"]; cache != nil && cache.timing.ReadyAt != nil {
		warm := 1.0
		if cache.status == "warming" && cache.spec.WarmupSeconds > 0 {
			elapsed := now - cache.stageStart
			warm = math.Max(0, math.Min(1, float64(elapsed)/float64(cache.spec.WarmupSeconds)))
		}
		hitRate = 0.45 + 0.43*warm
		if cache.timing.WarmedAt != nil {
			hitRate = 0.88
		}
	} else {
		hitRate = 0.05
	}

	for _, id := range e.order {
		s := e.services[id]
		switch s.status {
		case "starting":
			dbQPS += s.spec.StartupDBQPS
		case "probing":
			dbQPS += s.spec.StartupDBQPS * 0.35
		case "warming":
			dbQPS += s.spec.WarmupDBQPS
			cacheQPS += s.spec.WarmupDBQPS
		case "reconciling":
			dbQPS += 7.5
		case "open":
			dbQPS += 0.12 * s.trafficPct
			cacheQPS += 0.08 * s.trafficPct
		}
	}

	for _, site := range e.sites {
		activeTerminals += site.active
	}
	bizQPS := activeTerminals * 0.055
	dbQPS += bizQPS * (1 + 3.2*(1-hitRate))
	cacheQPS += bizQPS * 2.2
	return
}

func (e *Engine) sample(now int) Sample {
	dbQPS, cacheQPS, hit, active := e.pressureParts(now)
	starting, warming, reconciling, open := 0, 0, 0, 0
	connected := 0
	for _, s := range e.services {
		switch s.status {
		case "starting", "probing":
			starting++
		case "warming":
			warming++
		case "reconciling":
			reconciling++
		case "open":
			open++
		}
	}
	for _, st := range e.sites {
		connected += st.connected
	}
	genLoad := 0.0
	for _, m := range e.machines {
		if m.spec.GeneratorBacked && m.status == "up" || (m.spec.GeneratorBacked && m.status == "booting") {
			genLoad += m.spec.PowerKW
		}
	}
	return Sample{
		T: now, Mains: e.mains, Generator: !e.mains, VoltageStable: e.voltageStable,
		UPSChargePct: round2(e.upsCharge), UPSReady: e.upsReady,
		DBPressure:    round2(dbQPS / e.sc.DBQuotaQPS * 100),
		CachePressure: round2(cacheQPS / e.sc.CacheQuotaQPS * 100),
		DBQPS:         round2(dbQPS), CacheQPS: round2(cacheQPS), CacheHitRate: round2(hit),
		ConnectedTerminals: connected, ActiveTerminals: round2(active),
		StartingServices: starting, WarmingServices: warming, ReconcilingServices: reconciling, OpenServices: open,
		GeneratorLoadKW: round2(genLoad), GeneratorCapacityKW: e.sc.GeneratorKW,
	}
}

func (e *Engine) allComplete() bool {
	for _, s := range e.services {
		if s.timing.TrafficAt == nil {
			return false
		}
	}
	for _, st := range e.sites {
		if st.connected < st.spec.Terminals {
			return false
		}
	}
	return e.reconcileCompleted != nil
}

func (e *Engine) setWait(s *serviceState, reason string) {
	if reason == "" {
		e.clearWait(s)
		return
	}
	if s.waitReason == reason {
		return
	}
	e.clearWait(s)
	s.waitReason = reason
	s.waitSince = e.now
	e.waits = append(e.waits, WaitInterval{ServiceID: s.spec.ID, Name: s.spec.Name, Start: s.waitSince, Reason: reason})
}

func (e *Engine) clearWait(s *serviceState) {
	if s.waitReason == "" {
		return
	}
	for i := len(e.waits) - 1; i >= 0; i-- {
		w := e.waits[i]
		if w.ServiceID == s.spec.ID && w.Reason == s.waitReason && w.End == 0 {
			e.waits[i].End = e.now
			break
		}
	}
	s.waitReason = ""
	s.waitSince = 0
}

func currentItemT(items []Item) int {
	if len(items) == 0 {
		return 0
	}
	return items[len(items)-1].T
}

func intPtr(v int) *int        { return &v }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (e *Engine) buildReport() *Report {
	// 未正常关闭的等待只可能出现在失败场景，截止到恢复完成或模拟上限。
	waitEnd := e.completeAt
	if waitEnd == 0 {
		waitEnd = MaxSimSeconds
	}
	for i := range e.waits {
		if e.waits[i].End == 0 {
			e.waits[i].End = waitEnd
		}
	}

	machineTimings := []MachineTiming{}
	for _, id := range e.sortedMachineIDs() {
		m := e.machines[id]
		m.timing.SpecStatus = m.status
		machineTimings = append(machineTimings, m.timing)
	}
	serviceTimings := []ServiceTiming{}
	for _, id := range e.order {
		s := e.services[id]
		s.timing.Status = s.status
		if s.waitReason != "" {
			s.timing.WaitReason = s.waitReason
		}
		serviceTimings = append(serviceTimings, s.timing)
	}

	maxDB, maxCache := 0.0, 0.0
	pressureViolations := []string{}
	powerViolations := []string{}
	for _, sm := range e.samples {
		maxDB = math.Max(maxDB, sm.DBPressure)
		maxCache = math.Max(maxCache, sm.CachePressure)
		if sm.DBPressure > PressureHardLimit+0.01 {
			pressureViolations = append(pressureViolations, fmt.Sprintf("t=%d DB pressure %.2f > %.0f", sm.T, sm.DBPressure, PressureHardLimit))
		}
		if sm.CachePressure > PressureHardLimit+0.01 {
			pressureViolations = append(pressureViolations, fmt.Sprintf("t=%d cache pressure %.2f > %.0f", sm.T, sm.CachePressure, PressureHardLimit))
		}
		if !sm.Mains && sm.GeneratorLoadKW > sm.GeneratorCapacityKW+0.01 {
			powerViolations = append(powerViolations, fmt.Sprintf("t=%d generator load %.1fkW > %.1fkW", sm.T, sm.GeneratorLoadKW, sm.GeneratorCapacityKW))
		}
	}

	depViolations := e.checkDependencies()
	complete := e.completeAt
	end := e.endAt
	if end == 0 {
		if complete > 0 {
			end = complete
		} else {
			end = MaxSimSeconds
		}
	}
	total := 0
	if complete > 0 {
		total = complete - e.sc.MainsAt
	}
	rec := e.reconcileReport
	if rec.StartedAt == nil {
		rec.StartedAt = e.reconcileStartedAt
	}
	if rec.CompletedAt == nil {
		rec.CompletedAt = e.reconcileCompleted
	}

	return &Report{
		Scenario:             e.sc,
		Items:                CanonicalItems(e.items),
		Samples:              e.samples,
		Waits:                e.waits,
		MachineTimings:       machineTimings,
		ServiceTimings:       serviceTimings,
		Reconciliation:       rec,
		RecoveryCompleteAt:   complete,
		TotalRecoverySeconds: total,
		MaxDBPressure:        round2(maxDB),
		MaxCachePressure:     round2(maxCache),
		DependencyViolations: depViolations,
		PressureViolations:   pressureViolations,
		PowerViolations:      powerViolations,
		Success:              complete > 0 && len(depViolations) == 0 && len(pressureViolations) == 0 && len(powerViolations) == 0,
		EndAt:                end,
	}
}

func (e *Engine) checkDependencies() []string {
	violations := []string{}
	for _, id := range e.order {
		s := e.services[id]
		if s.timing.FirstStartAt == nil {
			violations = append(violations, fmt.Sprintf("%s never started", id))
			continue
		}
		for _, depID := range s.spec.Deps {
			dep := e.services[depID]
			if dep == nil {
				violations = append(violations, fmt.Sprintf("%s missing dep %s", id, depID))
				continue
			}
			if dep.timing.FirstReadyAt == nil || dep.timing.ReadyAt == nil {
				violations = append(violations, fmt.Sprintf("%s started before %s ready", id, depID))
				continue
			}
			// 首次启动必须晚于依赖首次真就绪；再次停电重置后，最后一次启动也必须晚于依赖当前就绪点。
			if *s.timing.FirstStartAt <= *dep.timing.FirstReadyAt {
				violations = append(violations, fmt.Sprintf("%s first start t=%d not after %s first ready t=%d",
					id, *s.timing.FirstStartAt, depID, *dep.timing.FirstReadyAt))
			}
			if s.timing.LastStartAt != nil && *s.timing.LastStartAt <= *dep.timing.ReadyAt {
				violations = append(violations, fmt.Sprintf("%s last start t=%d not after %s current ready t=%d",
					id, *s.timing.LastStartAt, depID, *dep.timing.ReadyAt))
			}
		}
		if s.spec.RequiresReconcile && s.timing.TrafficAt != nil && e.reconcileCompleted != nil &&
			*s.timing.TrafficAt < *e.reconcileCompleted {
			violations = append(violations, "settlement opened before reconciliation")
		}
	}
	return violations
}

// CanonicalItems 对同一秒内的事件做确定排序，便于和参照实现逐项比较。
func CanonicalItems(in []Item) []Item {
	out := append([]Item{}, in...)
	rank := map[string]int{
		"grid_outage": 0, "generator_start": 1, "mains_lost": 2, "mains_restored": 3,
		"power_transfer": 4, "ups_ready": 5, "voltage_stable": 6,
		"machine_boot_start": 10, "machine_disk_failed": 11, "machine_restart": 12,
		"machine_quarantine": 13, "machine_graceful_stop": 14, "machine_power_off": 15,
		"machine_booted":       16,
		"standby_verify_start": 20, "standby_verified": 21, "standby_verify_failed": 22,
		"service_relocated": 23, "service_start": 30, "port_open": 31,
		"service_start_failed": 32, "service_start_interrupted": 33, "service_graceful_stop": 34,
		"readiness_probe_start": 35, "readiness_passed": 36,
		"warmup_start": 37, "warmup_done": 38,
		"reconcile_start": 40, "reconcile_done": 41, "reconcile_mismatch": 42, "manual_queue": 43,
		"traffic_open": 50, "terminals_disconnected": 59, "terminal_retry": 60, "terminal_connected": 61,
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].T != out[j].T {
			return out[i].T < out[j].T
		}
		ri, rj := rank[out[i].Kind], rank[out[j].Kind]
		if ri != rj {
			return ri < rj
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}
