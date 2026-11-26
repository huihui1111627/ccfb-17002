package acceptance

import "testing"

func TestFixedSeedScenariosMatchReferenceAndStaySafe(t *testing.T) {
	suite := RunSuite(64)
	if suite.Failed != 0 || !suite.AllPassed {
		for _, r := range suite.Results {
			if r.Success && r.ReferenceMatch && r.BudgetMet {
				continue
			}
			t.Errorf("variant %d failed: success=%v ref=%v budget=%v deps=%v pressure=%v power=%v diff=%s",
				r.Variant, r.Success, r.ReferenceMatch, r.BudgetMet,
				r.DependencyViolations, r.PressureViolations, r.PowerViolations, r.ReferenceDiff)
		}
		t.Fatalf("acceptance suite failed: %d/%d passed", suite.Passed, suite.Count)
	}
	t.Logf("recovery seconds min=%d p50=%d p95=%d max=%d avg=%d; maxDb=%.1f maxCache=%.1f",
		suite.MinRecoverySeconds, suite.P50RecoverySeconds, suite.P95RecoverySeconds,
		suite.MaxRecoverySeconds, suite.AvgRecoverySeconds, suite.MaxDBPressure, suite.MaxCachePressure)
}
