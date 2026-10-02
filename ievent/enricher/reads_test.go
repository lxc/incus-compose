package enricher

import (
	"testing"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
)

func TestIsVMWithoutAgent(t *testing.T) {
	t.Parallel()

	t.Run("container is never VM without agent", func(t *testing.T) {
		t.Parallel()

		inst := &incusapi.Instance{Type: "container"}
		state := &incusapi.InstanceState{Processes: -1}
		assert.False(t, isVMWithoutAgent(inst, state))
	})

	t.Run("VM with processes > 0 has agent", func(t *testing.T) {
		t.Parallel()

		inst := &incusapi.Instance{Type: "virtual-machine"}
		state := &incusapi.InstanceState{Processes: 10}
		assert.False(t, isVMWithoutAgent(inst, state))
	})

	t.Run("VM with volatile STARTED has agent", func(t *testing.T) {
		t.Parallel()

		inst := &incusapi.Instance{
			Type: "virtual-machine",
			InstancePut: incusapi.InstancePut{
				Config: map[string]string{"volatile.last_state.agent": "STARTED"},
			},
		}
		state := &incusapi.InstanceState{Processes: -1}
		assert.False(t, isVMWithoutAgent(inst, state))
	})

	t.Run("VM with no agent and processes -1 is VM without agent", func(t *testing.T) {
		t.Parallel()

		inst := &incusapi.Instance{
			Type: "virtual-machine",
			InstancePut: incusapi.InstancePut{
				Config: map[string]string{"volatile.last_state.power": "RUNNING"},
			},
		}
		state := &incusapi.InstanceState{Processes: -1}
		assert.True(t, isVMWithoutAgent(inst, state))
	})

	t.Run("nil inst or nil state", func(t *testing.T) {
		t.Parallel()

		assert.False(t, isVMWithoutAgent(nil, nil))
		inst := &incusapi.Instance{Type: "virtual-machine"}
		assert.True(t, isVMWithoutAgent(inst, nil))
	})
}
