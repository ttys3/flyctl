package machine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/superfly/flyctl/api"
)

var _ LeasableMachine = &mockLeasableMachine{}

type mockLeasableMachine struct {
	LeasableMachine
	machine *api.Machine
}

func (m *mockLeasableMachine) Machine() *api.Machine {
	return m.machine
}

func (m *mockLeasableMachine) ReleaseLease(context.Context) error {
	return nil
}

func TestRemoveMachines(t *testing.T) {
	testcases := []struct {
		name      string
		ms        *machineSet
		input     []LeasableMachine
		expect    []LeasableMachine
		expectErr error
	}{
		{
			name: "remove one",
			ms: &machineSet{
				machines: []LeasableMachine{
					&mockLeasableMachine{
						machine: &api.Machine{ID: "1"},
					},
					&mockLeasableMachine{
						machine: &api.Machine{ID: "2"},
					},
				},
			},
			input: []LeasableMachine{
				&mockLeasableMachine{
					machine: &api.Machine{ID: "1"},
				},
			},
			expect: []LeasableMachine{
				&mockLeasableMachine{
					machine: &api.Machine{ID: "2"},
				},
			},
		},
		{
			name: "remove all",
			ms: &machineSet{
				machines: []LeasableMachine{
					&mockLeasableMachine{
						machine: &api.Machine{ID: "1"},
					},
					&mockLeasableMachine{
						machine: &api.Machine{ID: "2"},
					},
				},
			},
			input: []LeasableMachine{
				&mockLeasableMachine{
					machine: &api.Machine{ID: "1"},
				},
				&mockLeasableMachine{
					machine: &api.Machine{ID: "2"},
				},
			},
			expect: []LeasableMachine{},
		},
		{
			name: "remove none",
			ms: &machineSet{
				machines: []LeasableMachine{
					&mockLeasableMachine{
						machine: &api.Machine{ID: "1"},
					},
					&mockLeasableMachine{
						machine: &api.Machine{ID: "2"},
					},
				},
			},
			input: []LeasableMachine{},
			expect: []LeasableMachine{
				&mockLeasableMachine{
					machine: &api.Machine{ID: "1"},
				},
				&mockLeasableMachine{
					machine: &api.Machine{ID: "2"},
				},
			},
		},
	}

	ctx := context.Background()
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			actualErr := tc.ms.RemoveMachines(ctx, tc.input)
			r.Equal(tc.expect, tc.ms.machines)
			r.Equal(tc.expectErr, actualErr)
		})
	}
}
