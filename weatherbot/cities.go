package main

var trackedLocations = []WeatherLocation{
	{
		Name:               "New York",
		SeriesTicker:       "KXHIGHNY",
		Lat:                40.7128,
		Lon:                -74.0060,
		Timezone:           "America/New_York",
		PolymarketKeywords: []string{"new york", "nyc"},
	},
	{
		Name:               "Chicago",
		SeriesTicker:       "KXHIGHCHI",
		Lat:                41.8781,
		Lon:                -87.6298,
		Timezone:           "America/Chicago",
		PolymarketKeywords: []string{"chicago"},
	},
	{
		Name:               "Miami",
		SeriesTicker:       "KXHIGHMIA",
		Lat:                25.7617,
		Lon:                -80.1918,
		Timezone:           "America/New_York",
		PolymarketKeywords: []string{"miami"},
	},
	{
		Name:               "Los Angeles",
		SeriesTicker:       "KXHIGHLA",
		Lat:                34.0522,
		Lon:                -118.2437,
		Timezone:           "America/Los_Angeles",
		PolymarketKeywords: []string{"los angeles", "la"},
	},
	{
		Name:               "Denver",
		SeriesTicker:       "KXHIGHDEN",
		Lat:                39.7392,
		Lon:                -104.9903,
		Timezone:           "America/Denver",
		PolymarketKeywords: []string{"denver"},
	},
	{
		Name:               "Austin",
		SeriesTicker:       "KXHIGHAUS",
		Lat:                30.2672,
		Lon:                -97.7431,
		Timezone:           "America/Chicago",
		PolymarketKeywords: []string{"austin"},
	},
	{
		Name:               "Atlanta",
		SeriesTicker:       "KXHIGHATL",
		Lat:                33.7490,
		Lon:                -84.3880,
		Timezone:           "America/New_York",
		PolymarketKeywords: []string{"atlanta"},
	},
	{
		Name:               "Philadelphia",
		SeriesTicker:       "KXHIGHPHL",
		Lat:                39.9526,
		Lon:                -75.1652,
		Timezone:           "America/New_York",
		PolymarketKeywords: []string{"philadelphia", "philly"},
	},
}

func findLocationBySeries(series string) (WeatherLocation, bool) {
	for _, loc := range trackedLocations {
		if loc.SeriesTicker == series {
			return loc, true
		}
	}
	return WeatherLocation{}, false
}
