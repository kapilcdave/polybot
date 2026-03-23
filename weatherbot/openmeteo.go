package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type OpenMeteoClient struct {
	baseURL      string
	forecastDays int
	http         *http.Client
}

func NewOpenMeteoClient(cfg Config) *OpenMeteoClient {
	return &OpenMeteoClient{
		baseURL:      cfg.OpenMeteoURL,
		forecastDays: cfg.ForecastDays,
		http:         &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *OpenMeteoClient) EstimateHighAbove(ctx context.Context, loc WeatherLocation, eventDate time.Time, thresholdF float64) (ForecastEstimate, error) {
	query := url.Values{}
	query.Set("latitude", fmt.Sprintf("%.4f", loc.Lat))
	query.Set("longitude", fmt.Sprintf("%.4f", loc.Lon))
	query.Set("models", "gfs_seamless")
	query.Set("daily", "temperature_2m_max")
	query.Set("temperature_unit", "fahrenheit")
	query.Set("timezone", loc.Timezone)
	query.Set("forecast_days", fmt.Sprintf("%d", c.forecastDays))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+query.Encode(), nil)
	if err != nil {
		return ForecastEstimate{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ForecastEstimate{}, fmt.Errorf("open-meteo fetch: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Daily map[string]json.RawMessage `json:"daily"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ForecastEstimate{}, fmt.Errorf("open-meteo decode: %w", err)
	}

	times, err := decodeStringSlice(payload.Daily["time"])
	if err != nil {
		return ForecastEstimate{}, err
	}
	idx := indexForDate(times, eventDate.In(loadLocation(loc.Timezone)))
	if idx < 0 {
		return ForecastEstimate{}, fmt.Errorf("no forecast entry for %s", eventDate.Format("2006-01-02"))
	}

	memberSeries := collectMemberSeries(payload.Daily, "temperature_2m_max")
	if len(memberSeries) == 0 {
		return ForecastEstimate{}, fmt.Errorf("no ensemble member data returned")
	}
	maxTemps := make([]float64, 0, len(memberSeries))
	hits := 0
	for _, series := range memberSeries {
		if idx >= len(series) {
			continue
		}
		temp := series[idx]
		maxTemps = append(maxTemps, temp)
		if temp >= thresholdF {
			hits++
		}
	}
	if len(maxTemps) == 0 {
		return ForecastEstimate{}, fmt.Errorf("ensemble data missing forecast day")
	}
	sort.Float64s(maxTemps)
	return ForecastEstimate{
		City:          loc.Name,
		EventDate:     eventDate,
		ThresholdF:    thresholdF,
		ProbYes:       float64(hits) / float64(len(maxTemps)),
		Members:       len(maxTemps),
		MaxTemps:      maxTemps,
		Model:         "gfs_seamless",
		ForecastedAt:  time.Now().UTC(),
		SettlementRef: "Open-Meteo ensemble daily temperature_2m_max",
	}, nil
}

func decodeStringSlice(raw json.RawMessage) ([]string, error) {
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode date array: %w", err)
	}
	return out, nil
}

func collectMemberSeries(fields map[string]json.RawMessage, prefix string) [][]float64 {
	keys := make([]string, 0)
	for key := range fields {
		if key == prefix || strings.HasPrefix(key, prefix+"_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var series [][]float64
	for _, key := range keys {
		var flat []float64
		if err := json.Unmarshal(fields[key], &flat); err == nil && len(flat) > 0 {
			series = append(series, flat)
			continue
		}
		var nested [][]float64
		if err := json.Unmarshal(fields[key], &nested); err == nil {
			series = append(series, nested...)
		}
	}
	return series
}

func indexForDate(values []string, day time.Time) int {
	target := day.Format("2006-01-02")
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}
