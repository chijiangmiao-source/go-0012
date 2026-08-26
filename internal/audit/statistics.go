package audit

type CoveragePart struct {
	AreaSquareNM        float64
	CoverageBasisPoints int
}

func WeightedCoverageBasisPoints(parts []CoveragePart) int {
	var weighted float64
	var area float64
	for _, part := range parts {
		if part.AreaSquareNM <= 0 {
			continue
		}
		weighted += part.AreaSquareNM * float64(part.CoverageBasisPoints)
		area += part.AreaSquareNM
	}
	if area == 0 {
		return 0
	}
	return int(weighted/area + 0.5)
}

type ReviewSummary struct {
	TaskID              int64   `json:"task_id"`
	ActiveSectorVersion int64   `json:"active_sector_set_version"`
	CoverageBasisPoints int     `json:"coverage_basis_points"`
	Events              []Event `json:"events"`
	UnresolvedAlerts    int     `json:"unresolved_alerts"`
}
