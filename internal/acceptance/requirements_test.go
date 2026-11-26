package acceptance

import (
	"strings"
	"testing"

	shepherd "shepherd/internal/shepherd"
)

func TestEveryScenarioExercisesAllRecoveryRules(t *testing.T) {
	for v := 0; v < 64; v++ {
		sc := shepherd.GenerateScenario(v, shepherd.FixedSeedBase)
		rep := shepherd.Run(sc)
		if !rep.Success || rep.RecoveryCompleteAt == 0 {
			t.Fatalf("variant %d did not recover: deps=%v pressure=%v power=%v",
				v, rep.DependencyViolations, rep.PressureViolations, rep.PowerViolations)
		}

		itemsByKind := map[string][]shepherd.Item{}
		for _, it := range rep.Items {
			itemsByKind[it.Kind] = append(itemsByKind[it.Kind], it)
		}
		firstItem := func(kind, subject string) *shepherd.Item {
			for i := range rep.Items {
				it := rep.Items[i]
				if it.Kind == kind && (subject == "" || it.Subject == subject) {
					return &it
				}
			}
			return nil
		}

		// 1. 数据库和缓存必须通过各自声明的真实读写检查。
		for _, id := range []string{"db-primary", "cache-primary"} {
			passed := firstItem("readiness_passed", id)
			started := firstItem("readiness_probe_start", id)
			if passed == nil || started == nil || passed.T < started.T {
				t.Fatalf("variant %d %s missing real readiness probe/pass", v, id)
			}
		}

		stable := firstItem("voltage_stable", "grid")
		upsReady := firstItem("ups_ready", "ups")
		if stable == nil || upsReady == nil {
			t.Fatalf("variant %d missing voltage/UPS gate event", v)
		}

		// 2/6. 电压不稳时不启动有未落盘数据的服务；UPS 未达安全关机门槛时不启动非关键服务。
		for _, svc := range sc.Services {
			start := firstItem("service_start", svc.ID)
			if start == nil {
				t.Fatalf("variant %d service %s has no start event", v, svc.ID)
			}
			if !svc.AbruptSafe && start.T < stable.T {
				t.Fatalf("variant %d unsafe service %s started at %d before stable %d", v, svc.ID, start.T, stable.T)
			}
			if !svc.Critical && start.T < upsReady.T {
				t.Fatalf("variant %d noncritical service %s started at %d before UPS ready %d", v, svc.ID, start.T, upsReady.T)
			}
		}

		// 3. 结算前必须完成核对，异常单进入人工队列。
		reconcileDone := firstItem("reconcile_done", "settlement")
		settlement := findTiming(rep, "settlement")
		if reconcileDone == nil || settlement == nil || settlement.TrafficAt == nil || *settlement.TrafficAt < reconcileDone.T {
			t.Fatalf("variant %d settlement traffic opened before reconciliation", v)
		}
		if rep.Reconciliation.MismatchOrders == 0 || !rep.Reconciliation.HandedToManual || firstItem("manual_queue", "manual-console") == nil {
			t.Fatalf("variant %d mismatch orders were not handed to manual queue", v)
		}

		// 4. 发电机先带关键机器，且从不超载。
		if firstItem("generator_start", "generator") == nil || firstItem("mains_restored", "grid") == nil {
			t.Fatalf("variant %d missing generator/mains transfer events", v)
		}
		for _, id := range []string{"db-1", "db-2", "settle-1"} {
			m := findMachineTiming(rep, id)
			if m == nil || m.BootStartAt == nil || *m.BootStartAt != 0 {
				t.Fatalf("variant %d generator-backed machine %s did not boot at t=0", v, id)
			}
		}
		if len(rep.PowerViolations) != 0 {
			t.Fatalf("variant %d generator violations: %v", v, rep.PowerViolations)
		}

		// 5. 终端批次只能在网关和结算放流后接入；服务端返回过 Retry-After。
		if len(itemsByKind["terminal_retry"]) == 0 {
			t.Fatalf("variant %d has no coordinated terminal retry", v)
		}
		gateway := findTiming(rep, "terminal-gateway")
		if gateway == nil || gateway.TrafficAt == nil {
			t.Fatalf("variant %d terminal gateway never opened", v)
		}
		for _, it := range itemsByKind["terminal_connected"] {
			if *gateway.TrafficAt > it.T || *settlement.TrafficAt > it.T {
				t.Fatalf("variant %d terminal connected at %d before server ready", v, it.T)
			}
		}

		// 7. 重启环必须被隔离，服务漂移到指定的其他机器。
		var loopMachine *shepherd.MachineSpec
		var diskMachine *shepherd.MachineSpec
		for i := range sc.Machines {
			if sc.Machines[i].RestartLoop {
				loopMachine = &sc.Machines[i]
			}
			if sc.Machines[i].DiskFailed {
				diskMachine = &sc.Machines[i]
			}
		}
		if loopMachine == nil || firstItem("machine_quarantine", loopMachine.ID) == nil {
			t.Fatalf("variant %d restart loop was not quarantined", v)
		}
		for _, st := range rep.ServiceTimings {
			if st.OriginalMachine == loopMachine.ID && st.MachineID != loopMachine.MigrationTargetID {
				t.Fatalf("variant %d loop service %s on %s, want %s", v, st.ID, st.MachineID, loopMachine.MigrationTargetID)
			}
		}

		// 8. 坏盘机器必须切到预指定备用机，并核对最后同步版本。
		if diskMachine == nil || firstItem("machine_disk_failed", diskMachine.ID) == nil {
			t.Fatalf("variant %d disk failure was not detected", v)
		}
		verified := firstItem("standby_verified", diskMachine.DesignatedStandbyID)
		if verified == nil || !strings.Contains(verified.Detail, diskMachine.LastSyncVersion) {
			t.Fatalf("variant %d standby version was not verified against %s", v, diskMachine.LastSyncVersion)
		}
		for _, st := range rep.ServiceTimings {
			if st.OriginalMachine == diskMachine.ID && st.MachineID != diskMachine.DesignatedStandbyID {
				t.Fatalf("variant %d disk-fail service %s on %s, want %s", v, st.ID, st.MachineID, diskMachine.DesignatedStandbyID)
			}
		}
	}
}

func findTiming(r *shepherd.Report, id string) *shepherd.ServiceTiming {
	for i := range r.ServiceTimings {
		if r.ServiceTimings[i].ID == id {
			return &r.ServiceTimings[i]
		}
	}
	return nil
}

func findMachineTiming(r *shepherd.Report, id string) *shepherd.MachineTiming {
	for i := range r.MachineTimings {
		if r.MachineTimings[i].ID == id {
			return &r.MachineTimings[i]
		}
	}
	return nil
}
