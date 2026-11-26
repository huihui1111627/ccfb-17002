package shepherd

// 时间均以模拟秒表示，引擎按 TickSeconds 离散步进。
const (
	TickSeconds       = 5
	VoltageWindow     = 600
	UPSThresholdPct   = 80.0
	UPSChargePerSec   = 0.20
	UPSDrainPerSec    = 1.50
	ShutdownSeconds   = 20
	PressureHardLimit = 90.0
	PressureSoftLimit = 78.0
	MaxSimSeconds     = 3600
)

// Scenario 是一次固定、可复现的机房恢复演练输入。
type Scenario struct {
	ID            string         `json:"id"`
	Variant       int            `json:"variant"`
	Seed          int64          `json:"seed"`
	Name          string         `json:"name"`
	TickSeconds   int            `json:"tickSeconds"`
	MainsAt       int            `json:"mainsAt"`
	OutageStart   int            `json:"outageStart"`
	OutageEnd     int            `json:"outageEnd"`
	GeneratorKW   float64        `json:"generatorKW"`
	DBQuotaQPS    float64        `json:"dbQuotaQPS"`
	CacheQuotaQPS float64        `json:"cacheQuotaQPS"`
	Machines      []MachineSpec  `json:"machines"`
	Services      []ServiceSpec  `json:"services"`
	Sites         []TerminalSite `json:"sites"`
	Orders        []PendingOrder `json:"orders"`
	TerminalTotal int            `json:"terminalTotal"`
}

type MachineSpec struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Role                string  `json:"role"`
	PowerKW             float64 `json:"powerKW"`
	GeneratorBacked     bool    `json:"generatorBacked"`
	BootSeconds         int     `json:"bootSeconds"`
	Slow                bool    `json:"slow"`
	DiskFailed          bool    `json:"diskFailed"`
	DiskDetectSeconds   int     `json:"diskDetectSeconds"`
	DesignatedStandbyID string  `json:"designatedStandbyId,omitempty"`
	LastSyncVersion     string  `json:"lastSyncVersion,omitempty"`
	StandbyDataVersion  string  `json:"standbyDataVersion,omitempty"`
	RestartLoop         bool    `json:"restartLoop"`
	RestartAt           []int   `json:"restartAt,omitempty"`
	MigrationTargetID   string  `json:"migrationTargetId,omitempty"`
}

type ServiceSpec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	MachineID string `json:"machineId"`
	Layer     int    `json:"layer"`
	Critical  bool   `json:"critical"`
	// AbruptSafe 表示立即断电也不会丢失已确认数据；缓存可重建也属于此类。
	AbruptSafe        bool     `json:"abruptSafe"`
	ReadinessKind     string   `json:"readinessKind"`
	ReadinessProbe    string   `json:"readinessProbe"`
	Deps              []string `json:"deps,omitempty"`
	StartSeconds      int      `json:"startSeconds"`
	ProbeSeconds      int      `json:"probeSeconds"`
	WarmupSeconds     int      `json:"warmupSeconds"`
	TransientFailures int      `json:"transientFailures"`
	RequiresReconcile bool     `json:"requiresReconcile"`
	External          bool     `json:"external"`
	StartupDBQPS      float64  `json:"startupDbQps"`
	WarmupDBQPS       float64  `json:"warmupDbQps"`
}

type TerminalSite struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Terminals int    `json:"terminals"`
}

type PendingOrder struct {
	ID       string `json:"id"`
	SiteID   string `json:"siteId"`
	SiteName string `json:"siteName"`
	Item     string `json:"item"`
	Expected int    `json:"expectedKg"`
	Actual   int    `json:"actualKg"`
	Mismatch bool   `json:"mismatch"`
	Reason   string `json:"reason"`
}

// Item 是用于和参照实现逐项比较的规范时间线条目。
type Item struct {
	T       int    `json:"t"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
}

type Sample struct {
	T                   int     `json:"t"`
	Mains               bool    `json:"mains"`
	Generator           bool    `json:"generator"`
	VoltageStable       bool    `json:"voltageStable"`
	UPSChargePct        float64 `json:"upsChargePct"`
	UPSReady            bool    `json:"upsReady"`
	DBPressure          float64 `json:"dbPressure"`
	CachePressure       float64 `json:"cachePressure"`
	DBQPS               float64 `json:"dbQps"`
	CacheQPS            float64 `json:"cacheQps"`
	CacheHitRate        float64 `json:"cacheHitRate"`
	ConnectedTerminals  int     `json:"connectedTerminals"`
	ActiveTerminals     float64 `json:"activeTerminals"`
	StartingServices    int     `json:"startingServices"`
	WarmingServices     int     `json:"warmingServices"`
	ReconcilingServices int     `json:"reconcilingServices"`
	OpenServices        int     `json:"openServices"`
	GeneratorLoadKW     float64 `json:"generatorLoadKw"`
	GeneratorCapacityKW float64 `json:"generatorCapacityKw"`
}

type WaitInterval struct {
	ServiceID string `json:"serviceId"`
	Name      string `json:"name"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Reason    string `json:"reason"`
}

type MachineTiming struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	SpecStatus     string `json:"specStatus"`
	BootStartAt    *int   `json:"bootStartAt,omitempty"`
	BootedAt       *int   `json:"bootedAt,omitempty"`
	DiskFailedAt   *int   `json:"diskFailedAt,omitempty"`
	QuarantinedAt  *int   `json:"quarantinedAt,omitempty"`
	FinalMachineID string `json:"finalMachineId"`
}

type ServiceTiming struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Layer           int    `json:"layer"`
	Critical        bool   `json:"critical"`
	AbruptSafe      bool   `json:"abruptSafe"`
	OriginalMachine string `json:"originalMachineId"`
	MachineID       string `json:"machineId"`
	Status          string `json:"status"`
	FirstStartAt    *int   `json:"firstStartAt,omitempty"`
	LastStartAt     *int   `json:"lastStartAt,omitempty"`
	PortOpenAt      *int   `json:"portOpenAt,omitempty"`
	FirstReadyAt    *int   `json:"firstReadyAt,omitempty"`
	ReadyAt         *int   `json:"readyAt,omitempty"`
	WarmedAt        *int   `json:"warmedAt,omitempty"`
	ReconciledAt    *int   `json:"reconciledAt,omitempty"`
	TrafficAt       *int   `json:"trafficAt,omitempty"`
	Attempts        int    `json:"attempts"`
	TransientLeft   int    `json:"-"`
	WaitReason      string `json:"waitReason,omitempty"`
}

type ReconcileReport struct {
	StartedAt      *int           `json:"startedAt,omitempty"`
	CompletedAt    *int           `json:"completedAt,omitempty"`
	TotalOrders    int            `json:"totalOrders"`
	MatchedOrders  int            `json:"matchedOrders"`
	MismatchOrders int            `json:"mismatchOrders"`
	Mismatches     []PendingOrder `json:"mismatches"`
	HandedToManual bool           `json:"handedToManual"`
}

type Report struct {
	Scenario             Scenario        `json:"scenario"`
	Items                []Item          `json:"items"`
	Samples              []Sample        `json:"samples"`
	Waits                []WaitInterval  `json:"waits"`
	MachineTimings       []MachineTiming `json:"machineTimings"`
	ServiceTimings       []ServiceTiming `json:"serviceTimings"`
	Reconciliation       ReconcileReport `json:"reconciliation"`
	RecoveryCompleteAt   int             `json:"recoveryCompleteAt"`
	TotalRecoverySeconds int             `json:"totalRecoverySeconds"`
	MaxDBPressure        float64         `json:"maxDbPressure"`
	MaxCachePressure     float64         `json:"maxCachePressure"`
	DependencyViolations []string        `json:"dependencyViolations"`
	PressureViolations   []string        `json:"pressureViolations"`
	PowerViolations      []string        `json:"powerViolations"`
	Success              bool            `json:"success"`
	EndAt                int             `json:"endAt"`
}
