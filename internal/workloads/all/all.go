// Package all imports every workload group so they register themselves.
package all

import (
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads/activity"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads/searchattr"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads/shape"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads/storage"
)
