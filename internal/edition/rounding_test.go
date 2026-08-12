package edition

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoundDecimalReproducesServerRounding pins the edition's rounding to
// CPython's round(x, n), which is what the backend uses. Every pair below is a
// real (Σvision_expected_correct, Σscored_images) shape where the previous
// implementation — float32 record values scaled by 1e4 and passed through
// math.Round — disagreed with the served summary in the 4th decimal.
func TestRoundDecimalReproducesServerRounding(t *testing.T) {
	cases := []struct {
		numerator float64
		images    float64
		want      float64
		wantJSON  string
	}{
		{15.4, 160, 0.0963, "0.0963"}, // float32 inputs answered 0.0962
		{4.1, 16, 0.2562, "0.2562"},   // scaled math.Round answered 0.2563
		{1106.762, 1240, 0.8925, "0.8925"},
		{2.533, 4, 0.6332, "0.6332"},
		{1853.0, 4000, 0.4632, "0.4632"},
		{301.563, 324, 0.9307, "0.9307"},
		{29.36, 45, 0.6524, "0.6524"}, // the shared package-report fixture
	}
	for _, c := range cases {
		got := roundDecimal(c.numerator/c.images, 4)
		assert.Equal(t, c.want, got, "%v/%v", c.numerator, c.images)

		// The summary field is float32 on the wire: narrowing must not
		// reintroduce digits the backend never published.
		encoded, err := json.Marshal(float32(got))
		require.NoError(t, err)
		assert.Equal(t, c.wantJSON, string(encoded), "%v/%v", c.numerator, c.images)
	}
}

func TestRound2ReproducesServerRounding(t *testing.T) {
	assert.Equal(t, float32(11.03), round2(11.03))
	assert.Equal(t, float32(4.67), round2(14.0/3.0))
	// Ties resolve to even, as round(x, 2) does, not away from zero.
	assert.Equal(t, float32(0.12), round2(0.125))
}
