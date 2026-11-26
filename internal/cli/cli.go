package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"shepherd/internal/acceptance"
	shepherd "shepherd/internal/shepherd"
	webui "shepherd/internal/web"
)

func Run() {
	addr := flag.String("addr", ":8080", "网页监听地址")
	web := flag.Bool("web", true, "启动带网页的编排模拟器")
	accept := flag.Bool("acceptance", false, "运行固定种子验收套件并输出统计")
	runs := flag.Int("runs", 64, "验收场景数量")
	variant := flag.Int("sim", -1, "只输出指定场景的摘要（0..63）")
	jsonOut := flag.Bool("json", false, "配合 -acceptance 输出 JSON")
	flag.Parse()

	if *accept {
		suite := acceptance.RunSuite(*runs)
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(suite)
		} else {
			printSuite(suite)
		}
		if !suite.AllPassed {
			os.Exit(1)
		}
		return
	}

	if *variant >= 0 {
		sc := shepherd.GenerateScenario(*variant, shepherd.FixedSeedBase)
		rep := shepherd.Run(sc)
		fmt.Printf("variant=%d seed=%d machines=%d services=%d terminals=%d\n", sc.Variant, sc.Seed, len(sc.Machines), len(sc.Services), sc.TerminalTotal)
		fmt.Printf("success=%v recovery=%ds (来电后) completeAt=%d maxDB=%.1f maxCache=%.1f items=%d\n",
			rep.Success, rep.TotalRecoverySeconds, rep.RecoveryCompleteAt, rep.MaxDBPressure, rep.MaxCachePressure, len(rep.Items))
		if !rep.Success {
			for _, x := range rep.DependencyViolations {
				fmt.Println("dependency:", x)
			}
			for _, x := range rep.PressureViolations {
				fmt.Println("pressure:", x)
			}
			os.Exit(1)
		}
		return
	}

	if *web {
		srv := &http.Server{Addr: *addr, Handler: webui.NewHandler(), ReadHeaderTimeout: 5 * time.Second}
		fmt.Printf("牧区合作社断电恢复编排器已启动: http://localhost%s\n", *addr)
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func printSuite(s acceptance.Suite) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "场景数\t通过\t参照一致\t失败\t最短s\tP50s\tP95s\t最长s\t平均s\tDB峰值\tCache峰值\n")
	fmt.Fprintf(w, "%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\t%.1f\n",
		s.Count, s.Passed, s.ReferenceMatches, s.Failed,
		s.MinRecoverySeconds, s.P50RecoverySeconds, s.P95RecoverySeconds, s.MaxRecoverySeconds, s.AvgRecoverySeconds,
		s.MaxDBPressure, s.MaxCachePressure)
	_ = w.Flush()
	for _, r := range s.Results {
		if r.Success && r.ReferenceMatch && r.BudgetMet {
			continue
		}
		fmt.Printf("FAIL variant=%d: %+v\n", r.Variant, r)
	}
	if s.AllPassed {
		fmt.Println("全部满足：压力未越上限、依赖顺序无违反、与参照实现时间线逐项一致、恢复时间在预算内。")
	}
}
