package scoring

import (
	"math"
	"testing"
	"time"
)

func TestRecencyScore(t *testing.T) {
	now := time.Now().UnixNano()
	lambda := 0.05

	// Zero days old -> recency score 1.0
	scoreZero := RecencyScore(now, now, lambda)
	if math.Abs(scoreZero-1.0) > 1e-6 {
		t.Errorf("expected 1.0 for zero days, got %f", scoreZero)
	}

	// 14 days old (~half life for lambda=0.05) -> exp(-0.05 * 14) = exp(-0.70) ~ 0.4965
	fourteenDaysAgo := now - int64(14*24*time.Hour)
	score14 := RecencyScore(fourteenDaysAgo, now, lambda)
	if math.Abs(score14-0.496585) > 1e-4 {
		t.Errorf("expected ~0.4966, got %f", score14)
	}
}

func TestNormalizeBM25(t *testing.T) {
	// SQLite FTS5 bm25 returns negative values. Higher absolute value = better match.
	rawRanks := []float64{-10.0, -5.0, -2.5}
	normalized := NormalizeBM25(rawRanks)

	if len(normalized) != 3 {
		t.Fatalf("expected 3 items, got %d", len(normalized))
	}
	if normalized[0] != 1.0 {
		t.Errorf("expected top rank to normalize to 1.0, got %f", normalized[0])
	}
	if normalized[1] != 0.5 {
		t.Errorf("expected second rank to normalize to 0.5, got %f", normalized[1])
	}
	if normalized[2] != 0.25 {
		t.Errorf("expected third rank to normalize to 0.25, got %f", normalized[2])
	}
}

func TestComposite(t *testing.T) {
	// (1.0 * 0.5) + (1.0 * 0.3) + (1.0 * 0.2) = 1.0
	scoreMax := Composite(1.0, 1.0, 1.0)
	if scoreMax != 1.0 {
		t.Errorf("expected 1.0, got %f", scoreMax)
	}

	// (0.0 * 0.5) + (0.5 * 0.3) + (1.0 * 0.2) = 0.15 + 0.2 = 0.35
	scoreMid := Composite(0.0, 0.5, 1.0)
	if scoreMid != 0.35 {
		t.Errorf("expected 0.35, got %f", scoreMid)
	}
}
