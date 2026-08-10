package browser

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen(t *testing.T) {
	err := Open("https://example.com")
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		require.NoError(t, err)
	} else {
		assert.Error(t, err)
		if err != nil {
			assert.NotEmpty(t, err.Error())
		}
	}
}

func TestOpenInvalidURLStillAttempts(t *testing.T) {
	err := Open("")
	_ = err
}

func TestErrNoDisplaySentinel(t *testing.T) {
	assert.Equal(t, "no display", ErrNoDisplay.Error())
	assert.True(t, ErrNoDisplay != nil)
}
