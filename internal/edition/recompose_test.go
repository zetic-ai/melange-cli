package edition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackageFixtureSummaryRecomposesFromPublishedRecords is the backend-vs-CLI
// agreement gate on the SHARED contract fixture: it proves the records the
// public package report publishes are SUFFICIENT to reproduce the summary the
// backend served, independent of which path FilterReport happens to take.
//
// This matters because the whole reason the contract publishes
// vision_expected_correct alongside scored_images — rather than only the
// per-run vision_accuracy rate — is so a client can pool the exact numerator
// itself. If the served pooled_vision_accuracy could not be rebuilt from the
// published pair, the extra field would be decoration and any client filtering
// the fleet would be guessing.
//
// The other package tests cannot establish this. The copy-through test asserts
// the served fields survive a filter that drops nothing, which is true even if
// the records disagree with them; the re-derivation tests run on hand-built
// bodies whose summaries were written to match. Only the shared fixture carries
// records and a summary that the BACKEND produced together.
//
// The sums are taken from the raw JSON as float64 deliberately: the generated
// summary fields are float32, and narrowing before pooling is exactly the
// precision loss internal/edition was fixed for. A failure here is a backend
// contract bug (published records that cannot reproduce the published summary),
// not a test to relax.
func TestPackageFixtureSummaryRecomposesFromPublishedRecords(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi", "fixtures", "get_package_report.json"))
	require.NoError(t, err)

	var fixture struct {
		Response struct {
			Body struct {
				Records []struct {
					Metric string      `json:"metric"`
					Value  json.Number `json:"value"`
				} `json:"records"`
				Summary struct {
					BestPerplexity       *json.Number `json:"best_perplexity"`
					PooledVisionAccuracy *json.Number `json:"pooled_vision_accuracy"`
					ScoredImages         *json.Number `json:"scored_images"`
				} `json:"summary"`
			} `json:"body"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	body := fixture.Response.Body

	number := func(t *testing.T, n json.Number) float64 {
		t.Helper()
		value, err := n.Float64()
		require.NoError(t, err)
		return value
	}
	values := func(t *testing.T, metric string) []float64 {
		t.Helper()
		out := []float64{}
		for _, record := range body.Records {
			if record.Metric == metric {
				out = append(out, number(t, record.Value))
			}
		}
		return out
	}

	numerators := values(t, metricVisionExpectedCorrect)
	images := values(t, metricScoredImages)
	perplexities := values(t, metricPerplexity)

	// Guard against a vacuous pass: a fixture that stopped publishing the
	// records would otherwise recompose "nothing" into "nothing".
	require.NotEmpty(t, numerators, "fixture must publish vision_expected_correct records")
	require.Len(t, images, len(numerators),
		"the backend emits one scored_images per vision_expected_correct")
	require.NotEmpty(t, perplexities, "fixture must publish perplexity records")
	require.NotNil(t, body.Summary.PooledVisionAccuracy)
	require.NotNil(t, body.Summary.ScoredImages)
	require.NotNil(t, body.Summary.BestPerplexity)

	var sumNumerator, sumImages float64
	for i := range numerators {
		sumNumerator += numerators[i]
		sumImages += images[i]
	}
	require.Greater(t, sumImages, 0.0)

	assert.Equal(t, number(t, *body.Summary.PooledVisionAccuracy),
		roundDecimal(sumNumerator/sumImages, 4),
		"Σ(vision_expected_correct)/Σ(scored_images) = %v/%v must equal the served pooled_vision_accuracy",
		sumNumerator, sumImages)
	assert.Equal(t, number(t, *body.Summary.ScoredImages), sumImages,
		"Σ(scored_images) must equal the served scored_images")
	assert.Equal(t, number(t, *body.Summary.BestPerplexity), minOf(perplexities),
		"min(perplexity) over the published records must equal the served best_perplexity")
}
