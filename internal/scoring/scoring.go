package scoring

import (
	"math"
	"time"
)

const (
	DefaultDecayLambda = 0.05
	WeightBM25         = 0.5
	WeightConfidence   = 0.3
	WeightRecency      = 0.2
)

// RecencyScore calculates the exponential recency decay: exp(-lambda * days_since_update).
func RecencyScore(updatedAtNano, nowNano int64, lambda float64) float64 {
	if lambda <= 0 {
		lambda = DefaultDecayLambda
	}
	diffNano := nowNano - updatedAtNano
	if diffNano < 0 {
		diffNano = 0
	}
	days := float64(diffNano) / float64(24*time.Hour)
	return math.Exp(-lambda * days)
}

// NormalizeBM25 converts SQLite FTS5 bm25 rank scores (where lower/more negative is better)
// into a normalized relevance in [0.0, 1.0] where higher is better.
func NormalizeBM25(rawRanks []float64) []float64 {
	n := len(rawRanks)
	if n == 0 {
		return nil
	}

	normalized := make([]float64, n)
	relevances := make([]float64, n)
	var maxRelevance float64

	for i, rank := range rawRanks {
		// SQLite bm25 returns negative values for matches (e.g. -5.2 is better than -1.1)
		rel := -rank
		if rel < 0 {
			rel = 0
		}
		relevances[i] = rel
		if rel > maxRelevance {
			maxRelevance = rel
		}
	}

	if maxRelevance <= 0 {
		for i := range normalized {
			normalized[i] = 1.0
		}
		return normalized
	}

	for i, rel := range relevances {
		normalized[i] = rel / maxRelevance
	}

	return normalized
}

// Composite calculates the final hybrid recall score:
// score = (bm25_rank_normalized * 0.5) + (confidence * 0.3) + (recency_score * 0.2)
func Composite(bm25Norm, confidence, recency float64) float64 {
	if confidence < 0 {
		confidence = 0
	} else if confidence > 1.0 {
		confidence = 1.0
	}

	if bm25Norm < 0 {
		bm25Norm = 0
	} else if bm25Norm > 1.0 {
		bm25Norm = 1.0
	}

	if recency < 0 {
		recency = 0
	} else if recency > 1.0 {
		recency = 1.0
	}

	score := (bm25Norm * WeightBM25) + (confidence * WeightConfidence) + (recency * WeightRecency)
	return math.Round(score*10000) / 10000 // Round to 4 decimal places for clean representation
}
