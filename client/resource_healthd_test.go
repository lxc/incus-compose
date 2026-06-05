package client

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthdOfflineClient(t *testing.T) {
	c := NewOfflineClient(context.Background(), "test")

	healthd, err := c.Resource(KindHealthd, "ic-healthd", &HealthdConfig{})

	require.NoError(t, err)
	assert.Equal(t, "ic-healthd", healthd.IncusName())
}

func TestHealthdInstanceChanged(t *testing.T) {
	c := NewOfflineClient(context.Background(), "test")
	healthd, err := newHealthd(c, "ic-healthd", &HealthdConfig{})
	require.NoError(t, err)

	inst, err := newInstance(c, "web", &InstanceConfig{})
	require.NoError(t, err)

	createdInst, err := newInstance(c, "created", &InstanceConfig{})
	require.NoError(t, err)
	createdInst.created = true

	healthdInst, err := newInstance(c, "ic-healthd", &InstanceConfig{})
	require.NoError(t, err)
	healthdInst.created = true

	tests := []struct {
		name   string
		action Action
		res    Resource
		err    error
		want   bool
	}{
		{
			name:   "ensure created instance",
			action: ActionEnsure,
			res:    createdInst,
			want:   true,
		},
		{
			name:   "ensure existing instance",
			action: ActionEnsure,
			res:    inst,
			want:   false,
		},
		{
			name:   "start instance",
			action: ActionStart,
			res:    inst,
			want:   true,
		},
		{
			name:   "stop instance",
			action: ActionStop,
			res:    inst,
			want:   true,
		},
		{
			name:   "delete instance",
			action: ActionDelete,
			res:    inst,
			want:   true,
		},
		{
			name:   "healthd instance ignored",
			action: ActionEnsure,
			res:    healthdInst,
			want:   false,
		},
		{
			name:   "failed action ignored",
			action: ActionStart,
			res:    inst,
			err:    errors.New("failed"),
			want:   false,
		},
		{
			name:   "non-instance ignored",
			action: ActionEnsure,
			res:    healthd,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, healthdInstanceChanged(healthd, tt.action, tt.res, tt.err))
		})
	}
}
