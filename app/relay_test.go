package app

import (
	"testing"

	"claude-squad/ui"

	"github.com/stretchr/testify/require"
)

func TestAutoRelayStopsAtLimit(t *testing.T) {
	h := &home{
		menu:      ui.NewMenu(),
		relayMode: relayAuto,
	}

	for i := 1; i < autoRelayLimit; i++ {
		require.False(t, h.recordAutoRelay())
		require.Equal(t, relayAuto, h.relayMode)
		require.Equal(t, i, h.autoRelayCount)
	}

	require.True(t, h.recordAutoRelay())
	require.Equal(t, relayManual, h.relayMode)
	require.Equal(t, autoRelayLimit, h.autoRelayCount)
}
