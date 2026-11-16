// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/jointoken"
	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func newState() state.State {
	return state.WrapCore(inmem.NewState())
}

func createMachine(ctx context.Context, t *testing.T, st state.State, id string, connected bool, labels map[string]string) {
	t.Helper()

	status := omnires.NewMachineStatus(id)
	status.TypedSpec().Value.Connected = connected

	for k, v := range labels {
		status.Metadata().Labels().Set(k, v)
	}

	require.NoError(t, st.Create(ctx, status))
}

func TestFindMachine(t *testing.T) {
	t.Parallel()

	selector := map[string]string{"terraform/machine": "cp-0"}

	for _, test := range []struct {
		machines    func(context.Context, *testing.T, state.State)
		labels      map[string]string
		name        string
		id          string
		wantID      string
		wantSeen    string
		wantErrIs   error
		wantErrText string
	}{
		{
			name:     "nothing registered yet",
			labels:   selector,
			wantSeen: "no machine with the labels {terraform/machine=cp-0} has registered",
		},
		{
			name: "a connected machine with the labels",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", true, map[string]string{"terraform/machine": "cp-0", "other": "x"})
				createMachine(ctx, t, st, "m2", true, map[string]string{"terraform/machine": "cp-1"})
			},
			labels: selector,
			wantID: "m1",
		},
		{
			name: "every label has to match",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", true, map[string]string{"terraform/machine": "cp-0", "role": "worker"})
			},
			labels:   map[string]string{"terraform/machine": "cp-0", "role": "controlplane"},
			wantSeen: "no machine with the labels {role=controlplane, terraform/machine=cp-0} has registered",
		},
		{
			name: "registered but not connected is waited out",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", false, selector)
			},
			labels:   selector,
			wantSeen: "1 machine(s) with the labels {terraform/machine=cp-0} registered, none of them connected",
		},
		{
			// The machine of a replaced VM stays registered, disconnected, with the same labels.
			name: "a disconnected leftover does not get in the way of its replacement",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "old", false, selector)
				createMachine(ctx, t, st, "new", true, selector)
			},
			labels: selector,
			wantID: "new",
		},
		{
			name: "two connected machines are ambiguous",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", true, selector)
				createMachine(ctx, t, st, "m2", true, selector)
			},
			labels:      selector,
			wantErrIs:   omni.ErrMachineAmbiguous,
			wantErrText: "m1, m2",
		},
		{
			name:     "by ID, not registered",
			id:       "m1",
			wantSeen: `no machine "m1" has registered`,
		},
		{
			name: "by ID, not connected",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", false, nil)
			},
			id:       "m1",
			wantSeen: `machine "m1" has registered but is not connected`,
		},
		{
			name: "by ID, connected",
			machines: func(ctx context.Context, t *testing.T, st state.State) {
				createMachine(ctx, t, st, "m1", true, nil)
			},
			id:     "m1",
			wantID: "m1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			st := newState()

			if test.machines != nil {
				test.machines(ctx, t, st)
			}

			status, seen, err := omni.FindMachine(ctx, st, test.id, test.labels)

			if test.wantErrIs != nil {
				require.ErrorIs(t, err, test.wantErrIs)
				assert.Contains(t, err.Error(), test.wantErrText)

				return
			}

			require.NoError(t, err)

			if test.wantID == "" {
				assert.Nil(t, status)
				assert.Equal(t, test.wantSeen, seen)

				return
			}

			require.NotNil(t, status)
			assert.Equal(t, test.wantID, status.Metadata().ID())
		})
	}
}

func TestWaitForMachine(t *testing.T) {
	omni.SetWaitIntervals(t, time.Hour, time.Hour)

	selector := map[string]string{"terraform/machine": "cp-0"}

	t.Run("returns once the machine joins", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		go func() {
			time.Sleep(50 * time.Millisecond)

			createMachine(ctx, t, st, "m1", true, selector)
		}()

		status, err := omni.WaitForMachine(ctx, st, "", selector, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "m1", status.Metadata().ID())
	})

	t.Run("times out saying what it last saw", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createMachine(ctx, t, st, "m1", false, selector)

		_, err := omni.WaitForMachine(ctx, st, "", selector, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out after 50ms")
		assert.Contains(t, err.Error(), "none of them connected")
	})

	t.Run("ignores labels it was not asked for", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createMachine(ctx, t, st, "other", true, map[string]string{"terraform/machine": "cp-1"})

		_, err := omni.WaitForMachine(ctx, st, "", selector, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no machine with the labels")
	})
}

// brokenWatchState fails the first failWatches watches it is asked to open, and breaks the first
// watch it does open with an error event once breakAfter has passed.
type brokenWatchState struct {
	state.State

	opened      atomic.Int32
	failWatches int32
	breakAfter  time.Duration
}

func (s *brokenWatchState) WatchKind(ctx context.Context, kind resource.Kind, ch chan<- state.Event, opts ...state.WatchKindOption) error {
	attempt := s.opened.Add(1)

	if attempt <= s.failWatches {
		return errors.New("watch refused")
	}

	if attempt == s.failWatches+1 && s.breakAfter > 0 {
		go func() {
			select {
			case <-time.After(s.breakAfter):
				ch <- state.Event{Type: state.Errored, Error: errors.New("stream reset")}
			case <-ctx.Done():
			}
		}()

		// A broken stream delivers nothing more.
		return nil
	}

	return s.State.WatchKind(ctx, kind, ch, opts...)
}

func TestWaitWithBrokenWatches(t *testing.T) {
	selector := map[string]string{"terraform/machine": "cp-0"}

	for _, test := range []struct {
		st   *brokenWatchState
		name string
	}{
		{name: "keeps checking while the watch cannot be opened", st: &brokenWatchState{failWatches: 1 << 30}},
		{name: "reopens a watch that broke", st: &brokenWatchState{breakAfter: 20 * time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Without a working watch only the rewatch interval gets the wait to see the machine.
			omni.SetWaitIntervals(t, time.Hour, 50*time.Millisecond)

			ctx := t.Context()
			test.st.State = newState()

			go func() {
				time.Sleep(100 * time.Millisecond)

				createMachine(ctx, t, test.st.State, "m1", true, selector)
			}()

			status, err := omni.WaitForMachine(ctx, test.st, "", selector, 5*time.Second)
			require.NoError(t, err)
			assert.Equal(t, "m1", status.Metadata().ID())
			assert.GreaterOrEqual(t, test.st.opened.Load(), int32(2), "the watch should have been reopened")
		})
	}
}

func readyClusterStatus() *specs.ClusterStatusSpec {
	return &specs.ClusterStatusSpec{
		Phase:              specs.ClusterStatusSpec_RUNNING,
		Ready:              true,
		ControlplaneReady:  true,
		KubernetesAPIReady: true,
		Machines:           &specs.Machines{Total: 2, Healthy: 2, Requested: 2},
	}
}

func TestClusterNotReady(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		modify func(*specs.ClusterStatusSpec)
		name   string
		want   []string
	}{
		{
			name:   "ready",
			modify: func(*specs.ClusterStatusSpec) {},
		},
		{
			name: "scaling up",
			modify: func(s *specs.ClusterStatusSpec) {
				s.Phase = specs.ClusterStatusSpec_SCALING_UP
				s.Ready = false
			},
			want: []string{"phase is ScalingUp", "machine sets are not ready"},
		},
		{
			name: "control plane and API",
			modify: func(s *specs.ClusterStatusSpec) {
				s.ControlplaneReady = false
				s.KubernetesAPIReady = false
			},
			want: []string{"control plane is not ready", "Kubernetes API is not ready"},
		},
		{
			// A cluster with no machines can be reported ready, but nothing is running in it.
			name: "no machines",
			modify: func(s *specs.ClusterStatusSpec) {
				s.Machines = &specs.Machines{}
			},
			want: []string{"no machines are allocated"},
		},
		{
			// A node just added is requested before it is allocated, while the cluster still
			// reports ready from before.
			name: "requested machines not yet allocated",
			modify: func(s *specs.ClusterStatusSpec) {
				s.Machines.Requested = 3
			},
			want: []string{"2 of 3 requested machines are allocated"},
		},
		{
			name: "unhealthy machine",
			modify: func(s *specs.ClusterStatusSpec) {
				s.Machines.Healthy = 1
			},
			want: []string{"1 of 2 machines are healthy"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			spec := readyClusterStatus()
			test.modify(spec)

			assert.Equal(t, test.want, omni.ClusterNotReady(spec))
		})
	}
}

// createCluster creates cluster c1 at Talos 1.11.0 and Kubernetes 1.34.0, whose upgrade statuses
// report it has converged on them, and a status that reports it ready.
func createCluster(ctx context.Context, t *testing.T, st state.State) {
	t.Helper()

	const (
		id                = "c1"
		talosVersion      = "1.11.0"
		kubernetesVersion = "1.34.0"
	)

	cluster := omnires.NewCluster(id)
	cluster.TypedSpec().Value.TalosVersion = talosVersion
	cluster.TypedSpec().Value.KubernetesVersion = kubernetesVersion

	require.NoError(t, st.Create(ctx, cluster))

	talos := omnires.NewTalosUpgradeStatus(id)
	talos.TypedSpec().Value.Phase = specs.TalosUpgradeStatusSpec_Done
	talos.TypedSpec().Value.LastUpgradeVersion = talosVersion

	require.NoError(t, st.Create(ctx, talos))

	kubernetes := omnires.NewKubernetesUpgradeStatus(id)
	kubernetes.TypedSpec().Value.Phase = specs.KubernetesUpgradeStatusSpec_Done
	kubernetes.TypedSpec().Value.LastUpgradeVersion = kubernetesVersion

	require.NoError(t, st.Create(ctx, kubernetes))

	status := omnires.NewClusterStatus(id)
	status.TypedSpec().Value = readyClusterStatus()

	require.NoError(t, st.Create(ctx, status))
}

// createClusterMachine adds a machine to a cluster, with a status that reports it running and ready.
func createClusterMachine(ctx context.Context, t *testing.T, st state.State, cluster, id string) {
	t.Helper()

	machine := omnires.NewClusterMachine(id)
	machine.Metadata().Labels().Set(omnires.LabelCluster, cluster)

	require.NoError(t, st.Create(ctx, machine))

	status := omnires.NewClusterMachineStatus(id)
	status.Metadata().Labels().Set(omnires.LabelCluster, cluster)
	status.TypedSpec().Value.Stage = specs.ClusterMachineStatusSpec_RUNNING
	status.TypedSpec().Value.Ready = true

	require.NoError(t, st.Create(ctx, status))
}

func setMachineCounts(ctx context.Context, t *testing.T, st state.State, cluster string, total, healthy uint32) {
	t.Helper()

	_, err := safe.StateUpdateWithConflicts(ctx, st, omnires.NewClusterStatus(cluster).Metadata(), func(status *omnires.ClusterStatus) error {
		status.TypedSpec().Value.Machines = &specs.Machines{Total: total, Healthy: healthy, Requested: total}

		return nil
	})
	require.NoError(t, err)
}

// unavailableListState fails every list, as Omni does while it cannot be reached.
type unavailableListState struct {
	state.State
}

func (s *unavailableListState) List(context.Context, resource.Kind, ...state.ListOption) (resource.List, error) {
	return resource.List{}, grpcstatus.Error(codes.Unavailable, "connection refused")
}

func TestWaitForCluster(t *testing.T) {
	omni.SetWaitIntervals(t, time.Hour, time.Hour)

	t.Run("returns once the cluster is ready", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createCluster(ctx, t, st)

		_, err := safe.StateUpdateWithConflicts(ctx, st, omnires.NewClusterStatus("c1").Metadata(), func(status *omnires.ClusterStatus) error {
			status.TypedSpec().Value.Ready = false

			return nil
		})
		require.NoError(t, err)

		go func() {
			time.Sleep(50 * time.Millisecond)

			_, updateErr := safe.StateUpdateWithConflicts(ctx, st, omnires.NewClusterStatus("c1").Metadata(), func(status *omnires.ClusterStatus) error {
				status.TypedSpec().Value.Ready = true

				return nil
			})
			assert.NoError(t, updateErr)
		}()

		got, err := omni.WaitForCluster(ctx, st, "c1", nil, 5*time.Second)
		require.NoError(t, err)
		assert.True(t, got.TypedSpec().Value.GetReady())
	})

	t.Run("times out on a cluster that never gets a status", func(t *testing.T) {
		_, err := omni.WaitForCluster(t.Context(), newState(), "c1", nil, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `cluster "c1" has no status yet`)
	})

	// Updating the versions of a cluster only changes its spec: until Omni picks that up, the
	// cluster status and the upgrade statuses still describe the cluster as it was, ready.
	t.Run("waits for an upgrade to converge despite an old ready status", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createCluster(ctx, t, st)

		_, err := safe.StateUpdateWithConflicts(ctx, st, omnires.NewCluster("c1").Metadata(), func(cluster *omnires.Cluster) error {
			cluster.TypedSpec().Value.TalosVersion = "1.11.1"
			cluster.TypedSpec().Value.KubernetesVersion = "1.34.1"

			return nil
		})
		require.NoError(t, err)

		_, err = omni.WaitForCluster(ctx, st, "c1", nil, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Talos is at 1.11.0, not yet at 1.11.1")
		assert.Contains(t, err.Error(), "Kubernetes is at 1.34.0, not yet at 1.34.1")

		_, err = safe.StateUpdateWithConflicts(ctx, st, omnires.NewTalosUpgradeStatus("c1").Metadata(), func(status *omnires.TalosUpgradeStatus) error {
			status.TypedSpec().Value.Phase = specs.TalosUpgradeStatusSpec_Upgrading
			status.TypedSpec().Value.CurrentUpgradeVersion = "1.11.1"

			return nil
		})
		require.NoError(t, err)

		_, err = omni.WaitForCluster(ctx, st, "c1", nil, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Talos upgrade phase is Upgrading to 1.11.1")

		go func() {
			time.Sleep(50 * time.Millisecond)

			_, updateErr := safe.StateUpdateWithConflicts(ctx, st, omnires.NewTalosUpgradeStatus("c1").Metadata(), func(status *omnires.TalosUpgradeStatus) error {
				status.TypedSpec().Value.Phase = specs.TalosUpgradeStatusSpec_Done
				status.TypedSpec().Value.LastUpgradeVersion = "1.11.1"

				return nil
			})
			assert.NoError(t, updateErr)

			_, updateErr = safe.StateUpdateWithConflicts(ctx, st, omnires.NewKubernetesUpgradeStatus("c1").Metadata(), func(status *omnires.KubernetesUpgradeStatus) error {
				status.TypedSpec().Value.LastUpgradeVersion = "v1.34.1"

				return nil
			})
			assert.NoError(t, updateErr)
		}()

		_, err = omni.WaitForCluster(ctx, st, "c1", nil, 5*time.Second)
		require.NoError(t, err)
	})

	// A removed machine stays in the cluster, which still reports ready, until Omni has taken it out.
	t.Run("waits for removed machines to leave", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createCluster(ctx, t, st)

		for _, id := range []string{"m1", "m2"} {
			createClusterMachine(ctx, t, st, "c1", id)
		}

		_, err := omni.WaitForCluster(ctx, st, "c1", []string{"m1"}, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "still in the cluster: m2")

		_, err = omni.WaitForCluster(ctx, st, "c1", []string{"m1", "m2", "m3"}, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in the cluster yet: m3")

		require.NoError(t, st.Destroy(ctx, omnires.NewClusterMachine("m2").Metadata()))
		require.NoError(t, st.Destroy(ctx, omnires.NewClusterMachineStatus("m2").Metadata()))

		// The cluster status still describes the old membership.
		_, err = omni.WaitForCluster(ctx, st, "c1", []string{"m1"}, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the cluster reports 2 machines, not 1")

		go func() {
			time.Sleep(50 * time.Millisecond)

			setMachineCounts(ctx, t, st, "c1", 1, 1)
		}()

		_, err = omni.WaitForCluster(ctx, st, "c1", []string{"m1"}, 5*time.Second)
		require.NoError(t, err)
	})

	// A failed membership check must not pass for a successful one, or a scale down could end
	// without the removed machines having left.
	t.Run("retries when the cluster machines cannot be listed", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createCluster(ctx, t, st)

		for _, id := range []string{"m1", "m2"} {
			createClusterMachine(ctx, t, st, "c1", id)
		}

		setMachineCounts(ctx, t, st, "c1", 1, 1)

		_, err := omni.WaitForCluster(ctx, &unavailableListState{State: st}, "c1", []string{"m1"}, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Omni could not be reached")
	})

	// While scaling up, the added machine can be in the cluster before it runs, and the cluster
	// status can still describe the previous membership.
	t.Run("waits for added machines to be running and ready", func(t *testing.T) {
		ctx := t.Context()
		st := newState()

		createCluster(ctx, t, st)

		for _, id := range []string{"m1", "m2", "m3"} {
			createClusterMachine(ctx, t, st, "c1", id)
		}

		_, err := safe.StateUpdateWithConflicts(ctx, st, omnires.NewClusterMachineStatus("m3").Metadata(), func(status *omnires.ClusterMachineStatus) error {
			status.TypedSpec().Value.Stage = specs.ClusterMachineStatusSpec_INSTALLING
			status.TypedSpec().Value.Ready = false

			return nil
		})
		require.NoError(t, err)

		_, err = omni.WaitForCluster(ctx, st, "c1", []string{"m1", "m2", "m3"}, 50*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the cluster reports 2 machines, not 3")
		assert.Contains(t, err.Error(), "machines not running and ready: m3 (Installing)")

		go func() {
			time.Sleep(50 * time.Millisecond)

			_, updateErr := safe.StateUpdateWithConflicts(ctx, st, omnires.NewClusterMachineStatus("m3").Metadata(), func(status *omnires.ClusterMachineStatus) error {
				status.TypedSpec().Value.Stage = specs.ClusterMachineStatusSpec_RUNNING
				status.TypedSpec().Value.Ready = true

				return nil
			})
			assert.NoError(t, updateErr)

			setMachineCounts(ctx, t, st, "c1", 3, 3)
		}()

		_, err = omni.WaitForCluster(ctx, st, "c1", []string{"m1", "m2", "m3"}, 5*time.Second)
		require.NoError(t, err)
	})
}

func TestCheckJoinTokenCarriesLabels(t *testing.T) {
	t.Parallel()

	withLabels, err := jointoken.NewWithLabels("secret", map[string]string{"terraform/machine": "cp-0"}, nil)
	require.NoError(t, err)

	encoded, err := withLabels.Encode()
	require.NoError(t, err)

	api := func(token string) []string {
		return []string{"console=ttyS0", "siderolink.api=grpc://omni.example.com:8090?jointoken=" + url.QueryEscape(token)}
	}

	require.NoError(t, omni.CheckJoinTokenCarriesLabels(api(encoded)))

	// An Omni without label support returns the plain token it was asked to embed.
	err = omni.CheckJoinTokenCarriesLabels(api("secret"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "predates machine labels in join tokens")

	require.Error(t, omni.CheckJoinTokenCarriesLabels([]string{"console=ttyS0"}))
}

func TestWaitForMachineToLeaveCluster(t *testing.T) {
	omni.SetWaitIntervals(t, time.Hour, time.Hour)

	ctx := t.Context()
	st := newState()

	require.NoError(t, omni.WaitForMachineToLeaveCluster(ctx, st, "m1", time.Second), "a machine in no cluster has nothing to wait for")

	machine := omnires.NewClusterMachine("m1")
	machine.Metadata().Labels().Set(omnires.LabelCluster, "c1")

	require.NoError(t, st.Create(ctx, machine))

	err := omni.WaitForMachineToLeaveCluster(ctx, st, "m1", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `still in cluster "c1"`)

	go func() {
		time.Sleep(50 * time.Millisecond)

		assert.NoError(t, st.Destroy(ctx, machine.Metadata()))
	}()

	require.NoError(t, omni.WaitForMachineToLeaveCluster(ctx, st, "m1", 5*time.Second))
}
