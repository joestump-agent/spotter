package components

import "strconv"

// InitializeHourlyStats returns 24 pre-labelled zero-value ChartDataPoints for hours 0–23.
func InitializeHourlyStats() []ChartDataPoint {
	pts := make([]ChartDataPoint, 24)
	for i := range pts {
		pts[i] = ChartDataPoint{Label: strconv.Itoa(i), Value: 0}
	}
	return pts
}

// InitializeDailyStats returns 7 pre-labelled zero-value ChartDataPoints for Sun–Sat.
func InitializeDailyStats() []ChartDataPoint {
	days := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	pts := make([]ChartDataPoint, len(days))
	for i, d := range days {
		pts[i] = ChartDataPoint{Label: d, Value: 0}
	}
	return pts
}

// InitializeMonthlyStats returns 12 pre-labelled zero-value ChartDataPoints for Jan–Dec.
func InitializeMonthlyStats() []ChartDataPoint {
	months := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	pts := make([]ChartDataPoint, len(months))
	for i, m := range months {
		pts[i] = ChartDataPoint{Label: m, Value: 0}
	}
	return pts
}
