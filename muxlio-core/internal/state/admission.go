package state

// AdmissionConfig declares shared resource budgets. Zero caps are unlimited.
// Provider names are opaque operator-supplied resource keys, never inferred
// from adapter names. Rate tokens measure dispatch starts, not token usage.
type AdmissionConfig struct {
	GlobalCap     int                        `json:"globalCap,omitempty"`
	PerRunCap     int                        `json:"perRunCap,omitempty"`
	ExecutorCaps  map[string]int             `json:"executorCaps,omitempty"`
	KeyCaps       map[string]int             `json:"keyCaps,omitempty"`
	ProviderCaps  map[string]int             `json:"providerCaps,omitempty"`
	ProviderRates map[string]AdmissionRate   `json:"providerRates,omitempty"`
	Executors     map[string]AdmissionDemand `json:"executors,omitempty"`
}

// AdmissionDemand assigns a fixed slot cost and shared resource keys to an
// executor. Slots default to one and apply to every concurrency budget.
type AdmissionDemand struct {
	ExecutorKey string `json:"executorKey,omitempty"`
	Slots       int    `json:"slots,omitempty"`
	OperatorKey string `json:"operatorKey,omitempty"`
	ProviderKey string `json:"providerKey,omitempty"`
}

type AdmissionRate struct {
	Burst     int     `json:"burst"`
	PerSecond float64 `json:"perSecond"`
}
