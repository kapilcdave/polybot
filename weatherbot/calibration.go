package main

var globalCal *CalibrationStore

// calibrateProb adjusts probability using simple beta-binomial reliability per city.
// Replace with isotonic regression or Platt scaling when enough resolved data exists.
func calibrateProb(prob float64, m Market) float64 {
	if prob <= 0 {
		return 0
	}
	if prob >= 1 {
		return 1
	}
	if globalCal == nil {
		return prob
	}
	alpha, beta := globalCal.ReliabilityBeta(m.City)
	return (prob*alpha + (1-prob)*beta) / (alpha + beta)
}
