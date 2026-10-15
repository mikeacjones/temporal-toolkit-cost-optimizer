// Package all imports every detector group so they register themselves.
package all

import (
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/activity"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/searchattr"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/shape"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/storage"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/timer"
)
