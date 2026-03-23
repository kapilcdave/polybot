package main

// SettlementStation holds the authoritative station used to settle a market.
type SettlementStation struct {
	City      string
	StationID string // e.g. "USW00094728" (Central Park)
	Source    string // e.g. "NWS Daily Climate Report"
	Timezone  string
	Lat, Lon  float64
}

var settlementStations = map[string]SettlementStation{
	"New York":     {City: "New York", StationID: "USW00094728", Source: "NWS DCR", Timezone: "America/New_York", Lat: 40.779, Lon: -73.969},
	"Chicago":      {City: "Chicago", StationID: "USW00094846", Source: "NWS DCR", Timezone: "America/Chicago", Lat: 41.995, Lon: -87.933},
	"Miami":        {City: "Miami", StationID: "USW00012839", Source: "NWS DCR", Timezone: "America/New_York", Lat: 25.793, Lon: -80.290},
	"Austin":       {City: "Austin", StationID: "USW00013904", Source: "NWS DCR", Timezone: "America/Chicago", Lat: 30.194, Lon: -97.67},
	"Los Angeles":  {City: "Los Angeles", StationID: "USW00023174", Source: "NWS DCR", Timezone: "America/Los_Angeles", Lat: 34.016, Lon: -118.149},
	"Denver":       {City: "Denver", StationID: "USW00023062", Source: "NWS DCR", Timezone: "America/Denver", Lat: 39.859, Lon: -104.671},
	"Atlanta":      {City: "Atlanta", StationID: "USW00013874", Source: "NWS DCR", Timezone: "America/New_York", Lat: 33.636, Lon: -84.428},
	"Philadelphia": {City: "Philadelphia", StationID: "USW00013739", Source: "NWS DCR", Timezone: "America/New_York", Lat: 39.872, Lon: -75.24},
}

func settlementForCity(city string) (SettlementStation, bool) {
	s, ok := settlementStations[city]
	return s, ok
}
