// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"
	"fmt"
	"time"

	cosiresource "github.com/cosi-project/runtime/pkg/resource"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// waitResyncInterval is how often a wait re-checks Omni with no event to prompt it. The watches
// should make it unnecessary, but a stream that stalls without reporting an error would otherwise
// leave the wait blind until it times out.
var waitResyncInterval = 30 * time.Second

// waitRewatchInterval is how long a wait holds off before reopening watches that failed.
var waitRewatchInterval = 5 * time.Second

// waitEventBuffer lets a watch deliver a burst, such as the initial contents of a kind, without
// blocking on the wait, which only needs to know that something changed.
const waitEventBuffer = 64

// watchFunc opens one watch, delivering its events to the channel until ctx is canceled.
type watchFunc func(ctx context.Context, st cosistate.State, events chan<- cosistate.Event) error

// watchResource watches a single resource. It need not exist yet.
func watchResource(ptr cosiresource.Pointer) watchFunc {
	return func(ctx context.Context, st cosistate.State, events chan<- cosistate.Event) error {
		return st.Watch(ctx, ptr, events)
	}
}

// watchKind watches every resource of a kind that matches opts.
func watchKind(kind cosiresource.Kind, opts ...cosistate.WatchKindOption) watchFunc {
	return func(ctx context.Context, st cosistate.State, events chan<- cosistate.Event) error {
		return st.WatchKind(ctx, kind, events, opts...)
	}
}

// waitUntil runs check until it reports done, returns an error, or ctx ends, in which case it
// returns ctx's error.
//
// The events of the watches only prompt a check, which reads what it needs from Omni itself: the
// checks stay simple, and nothing is lost when a watch drops and has to be reopened. Should the
// watches fail to open, or break, the wait goes on checking every waitRewatchInterval while it tries
// to reopen them.
func waitUntil(ctx context.Context, st cosistate.State, watches []watchFunc, check func(context.Context) (bool, error)) error {
	var (
		events      chan cosistate.Event
		cancelWatch context.CancelFunc = func() {}
		lastFailure time.Time
	)

	defer func() { cancelWatch() }()

	for {
		if events == nil && time.Since(lastFailure) >= waitRewatchInterval {
			var err error

			events, cancelWatch, err = openWatches(ctx, st, watches)
			if err != nil {
				tflog.Debug(ctx, "failed to watch Omni, checking periodically instead", map[string]any{"error": err.Error()})

				lastFailure = time.Now()
			}
		}

		// Checking only once the watches are open means no change can slip in between the two.
		done, err := check(ctx)
		if err != nil || done {
			return err
		}

		interval := waitResyncInterval
		if events == nil {
			interval = waitRewatchInterval
		}

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return ctx.Err()
		case event := <-events:
			if watchErr := drain(event, events); watchErr != nil {
				tflog.Debug(ctx, "watch on Omni broke, reopening it", map[string]any{"error": watchErr.Error()})

				cancelWatch()

				events, lastFailure = nil, time.Now()
			}
		case <-timer.C:
		}

		timer.Stop()
	}
}

// openWatches opens all the watches onto one fresh channel, so that events from watches closed
// earlier never mix in.
func openWatches(ctx context.Context, st cosistate.State, watches []watchFunc) (chan cosistate.Event, context.CancelFunc, error) {
	events := make(chan cosistate.Event, waitEventBuffer)

	watchCtx, cancel := context.WithCancel(ctx)

	for _, watch := range watches {
		if err := watch(watchCtx, st, events); err != nil {
			cancel()

			return nil, func() {}, err
		}
	}

	return events, cancel, nil
}

// drain consumes the events already queued after first, since a single check covers all of them,
// and returns the error of the first one reporting a broken watch.
func drain(first cosistate.Event, events chan cosistate.Event) error {
	event := first

	for {
		if event.Type == cosistate.Errored {
			if event.Error != nil {
				return event.Error
			}

			return fmt.Errorf("the watch failed")
		}

		select {
		case event = <-events:
		default:
			return nil
		}
	}
}
