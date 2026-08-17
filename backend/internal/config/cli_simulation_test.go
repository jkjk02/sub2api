package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCliSimulationConfigEffectiveProtocolMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty keeps compatibility", raw: "", want: CliSimulationProtocolModeLegacy},
		{name: "legacy normalized", raw: " LEGACY ", want: CliSimulationProtocolModeLegacy},
		{name: "passthrough normalized", raw: " PassThrough ", want: CliSimulationProtocolModePassthrough},
		{name: "unknown keeps compatibility", raw: "future-mode", want: CliSimulationProtocolModeLegacy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := CliSimulationConfig{ProtocolMode: tt.raw}
			require.Equal(t, tt.want, cfg.EffectiveProtocolMode())
		})
	}
}

func TestCliSimulationConfigLegacySynthesisEnabled(t *testing.T) {
	t.Parallel()

	require.True(t, (CliSimulationConfig{Enabled: true}).LegacySynthesisEnabled())
	require.True(t, (CliSimulationConfig{Enabled: true, ProtocolMode: "legacy"}).LegacySynthesisEnabled())
	require.False(t, (CliSimulationConfig{Enabled: false, ProtocolMode: "legacy"}).LegacySynthesisEnabled())
	require.False(t, (CliSimulationConfig{Enabled: true, ProtocolMode: "passthrough"}).LegacySynthesisEnabled())
}
