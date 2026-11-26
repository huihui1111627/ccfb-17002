package acceptance

import (
	"fmt"
	"reflect"
	"sort"

	"shepherd/internal/reference"
	shepherd "shepherd/internal/shepherd"
)

const RecoveryBudgetSeconds = 3600

type Result struct {
	Variant              int      `json:"variant"`
	Seed                 int64    `json:"seed"`
	Name                 string   `json:"name"`
	Machines             int      `json:"machines"`
	Services             int      `json:"services"`
	Terminals            int      `json:"terminals"`
	Items                int      `json:"items"`
	Success              bool     `json:"success"`
	ReferenceMatch       bool     `json:"referenceMatch"`
	TotalRecoverySeconds int      `json:"totalRecoverySeconds"`
	RecoveryCompleteAt   int      `json:"recoveryCompleteAt"`
	MaxDBPressure        float64  `json:"maxDbPressure"`
	MaxCachePressure     float64  `json:"maxCachePressure"`
	DependencyViolations []string `json:"dependencyViolations"`
	PressureViolations   []string `json:"pressureViolations"`
	PowerViolations      []string `json:"powerViolations"`
	ReferenceDiff        string   `json:"referenceDiff,omitempty"`
	BudgetMet            bool     `json:"budgetMet"`
}

type Suite struct {
	Count              int      `json:"count"`
	Passed             int      `json:"passed"`
	Failed             int      `json:"failed"`
	ReferenceMatches   int      `json:"referenceMatches"`
	AllPassed          bool     `json:"allPassed"`
	MinRecoverySeconds int      `json:"minRecoverySeconds"`
	P50RecoverySeconds int      `json:"p50RecoverySeconds"`
	P95RecoverySeconds int      `json:"p95RecoverySeconds"`
	MaxRecoverySeconds int      `json:"maxRecoverySeconds"`
	AvgRecoverySeconds int      `json:"avgRecoverySeconds"`
	MaxDBPressure      float64  `json:"maxDbPressure"`
	MaxCachePressure   float64  `json:"maxCachePressure"`
	Results            []Result `json:"results"`
}

func RunSuite(count int) Suite {
	if count <= 0 {
		count = 64
	}
	suite := Suite{Count: count, Results: make([]Result, 0, count)}
	totals := []int{}
	sum := 0

	for v := 0; v < count; v++ {
		sc := shepherd.GenerateScenario(v, shepherd.FixedSeedBase)
		rep := shepherd.Run(sc)
		ref := reference.RunReference(sc)

		match, diff := CompareItems(rep.Items, ref.Items)
		res := Result{
			Variant: v, Seed: sc.Seed, Name: sc.Name,
			Machines: len(sc.Machines), Services: len(sc.Services), Terminals: sc.TerminalTotal,
			Items: len(rep.Items), Success: rep.Success, ReferenceMatch: match,
			TotalRecoverySeconds: rep.TotalRecoverySeconds, RecoveryCompleteAt: rep.RecoveryCompleteAt,
			MaxDBPressure: rep.MaxDBPressure, MaxCachePressure: rep.MaxCachePressure,
			DependencyViolations: rep.DependencyViolations, PressureViolations: rep.PressureViolations,
			PowerViolations: rep.PowerViolations,
			ReferenceDiff:   diff, BudgetMet: rep.Success && rep.TotalRecoverySeconds > 0 && rep.TotalRecoverySeconds <= RecoveryBudgetSeconds,
		}
		if res.DependencyViolations == nil {
			res.DependencyViolations = []string{}
		}
		if res.PressureViolations == nil {
			res.PressureViolations = []string{}
		}
		if res.PowerViolations == nil {
			res.PowerViolations = []string{}
		}
		if match {
			suite.ReferenceMatches++
		}
		if res.Success && match && res.BudgetMet && len(rep.DependencyViolations) == 0 &&
			len(rep.PressureViolations) == 0 && len(rep.PowerViolations) == 0 {
			suite.Passed++
		} else {
			suite.Failed++
		}
		totals = append(totals, rep.TotalRecoverySeconds)
		sum += rep.TotalRecoverySeconds
		suite.MaxDBPressure = maxFloat(suite.MaxDBPressure, rep.MaxDBPressure)
		suite.MaxCachePressure = maxFloat(suite.MaxCachePressure, rep.MaxCachePressure)
		suite.Results = append(suite.Results, res)
	}

	sort.Ints(totals)
	if len(totals) > 0 {
		suite.MinRecoverySeconds = totals[0]
		suite.P50RecoverySeconds = totals[len(totals)/2]
		idx := int(float64(len(totals)-1) * 0.95)
		suite.P95RecoverySeconds = totals[idx]
		suite.MaxRecoverySeconds = totals[len(totals)-1]
		suite.AvgRecoverySeconds = sum / len(totals)
	}
	suite.AllPassed = suite.Failed == 0 && suite.ReferenceMatches == count
	return suite
}

func CompareItems(got, want []shepherd.Item) (bool, string) {
	if reflect.DeepEqual(got, want) {
		return true, ""
	}
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if !reflect.DeepEqual(got[i], want[i]) {
			return false, fmt.Sprintf("第 %d 项不一致：主实现=%+v；参照实现=%+v", i, got[i], want[i])
		}
	}
	return false, fmt.Sprintf("时间线长度不一致：主实现 %d 项，参照实现 %d 项", len(got), len(want))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
