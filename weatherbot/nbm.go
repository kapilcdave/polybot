package main

import (
	"context"
	"fmt"
	"time"
)

// NBMClient is a placeholder for probabilistic forecasts from the National Blend of Models.
// Production implementation should read GRIB2 percentiles (p05, p10, p25, p50, p75, p90, p95) for MaxT.
type NBMClient struct{}

func NewNBMClient() *NBMClient { return &NBMClient{} }

// EstimateHighProb returns approximate P(high >= thresholdF) using cached/placeholder logic.
// TODO: replace with real GRIB2 percentile ingestion.
func (n *NBMClient) EstimateHighProb(ctx context.Context, loc WeatherLocation, eventDate time.Time, thresholdF float64) (float64, error) {
	return 0, fmt.Errorf("NBM not implemented: add GRIB2 percentile ingestion for %s %s", loc.Name, eventDate.Format("2006-01-02"))
}
