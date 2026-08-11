package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunHelp(t *testing.T) {
	assert.Equal(t, 0, Run([]string{"--help"}))
}

func TestRunVersion(t *testing.T) {
	assert.Equal(t, 0, Run([]string{"version"}))
}

func TestRunRejectsIOSDeploymentBeforeAuthentication(t *testing.T) {
	assert.Equal(t, 2, Run([]string{
		"deploy", "guide", "model-key", "-R", "account/repo", "--language", "ios-swift",
	}))
}

func TestRunUnknownCommandIsUsageError(t *testing.T) {
	assert.Equal(t, 2, Run([]string{"definitely-does-not-exist"}))
}
