/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// AllowlistFanout re-enqueues every workload of every reporting kind
// when spec.discovery.workloadKinds changes (Design 006 §2, §4).
//
// Hot-reload of the other config fields is lazy: the new snapshot is
// read on each workload's next reconcile, whenever that is. The
// allowlist cannot be lazy, because the operator's visible outcome is
// "documents for the removed kind are gone" and "the kind I added back
// is inventoried" — without a workload event nothing would happen
// until the informers' resync. So on an allowlist change each
// registered kind is listed once, through the uncached reader so the
// list never creates an informer on the shared cache for a kind the
// supervisor keeps off it, and every object is pushed through the
// kind's channel source. The reconcile path then applies the filter
// (outcome kind_not_allowed), re-roots children whose owner stopped
// being allowed, and re-absorbs children whose owner is allowed again.
//
// Supervised kinds that are not allowed are skipped: the supervisor has
// stopped their controller and swept their AIBOMs, and nothing is
// listening on their channel.
type AllowlistFanout struct {
	// Reader lists workloads; the manager's APIReader in production.
	Reader client.Reader
	// Timeout bounds one fan-out; default 2m.
	Timeout time.Duration

	mu    sync.Mutex
	kinds []*fanoutKind
	runMu sync.Mutex // one fan-out at a time
}

type fanoutKind struct {
	gk         schema.GroupKind
	supervised bool
	ch         chan event.GenericEvent
	list       func() client.ObjectList
	items      func(client.ObjectList) []client.Object
}

// fanoutBuffer is the per-kind channel depth. Managed kinds always have
// a reader (their controller runs for the manager's lifetime) so the
// buffer only smooths bursts; for supervised kinds a full buffer means
// the controller is mid-rebuild, and the rebuild's initial list
// re-reconciles everything anyway, so the send is dropped.
const fanoutBuffer = 1024

// fanoutPageSize bounds one list request during a fan-out.
const fanoutPageSize = 500

// Source registers gk and returns the channel source its controller
// watches. Called once per managed kind at setup and on every rebuild
// of a supervised kind (the channel is stable across rebuilds; only the
// source object is new).
func (f *AllowlistFanout) Source(gk schema.GroupKind, supervised bool, list func() client.ObjectList, items func(client.ObjectList) []client.Object) source.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.kinds {
		if k.gk == gk {
			return source.Channel(k.ch, &handler.EnqueueRequestForObject{})
		}
	}
	k := &fanoutKind{gk: gk, supervised: supervised, ch: make(chan event.GenericEvent, fanoutBuffer), list: list, items: items}
	f.kinds = append(f.kinds, k)
	return source.Channel(k.ch, &handler.EnqueueRequestForObject{})
}

// Bind subscribes to store so a workloadKinds change triggers a
// fan-out. The fan-out runs on its own goroutine: it lists the cluster
// and must not hold up the config reconciler.
func (f *AllowlistFanout) Bind(store *config.Store) {
	if f == nil || store == nil {
		return
	}
	store.Subscribe(func(prev, next *config.Snapshot) {
		if prev != nil && prev.WorkloadKinds.Equal(next.WorkloadKinds) {
			return
		}
		go f.Trigger(next.WorkloadKinds)
	})
}

// Trigger re-enqueues every workload of every registered kind that is
// allowed or managed. Exported for the envtest harness; production
// callers go through Bind.
func (f *AllowlistFanout) Trigger(allowed config.WorkloadKindSet) {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log := ctrl.Log.WithName("allowlist-fanout").WithValues("workloadKinds", allowed.String())

	f.mu.Lock()
	kinds := append([]*fanoutKind{}, f.kinds...)
	f.mu.Unlock()

	for _, k := range kinds {
		if k.supervised && !allowed.Allows(k.gk) {
			continue
		}
		sent, err := f.fanOutKind(ctx, k)
		if err != nil {
			log.Info("fan-out incomplete", "kind", k.gk.String(), "enqueued", sent, "err", err.Error())
			if ctx.Err() != nil {
				return
			}
			continue
		}
		log.Info("re-enqueued workloads after workloadKinds change", "kind", k.gk.String(), "count", sent)
	}
}

func (f *AllowlistFanout) fanOutKind(ctx context.Context, k *fanoutKind) (int, error) {
	sent := 0
	var continueToken string
	for {
		list := k.list()
		if err := f.Reader.List(ctx, list, client.Limit(fanoutPageSize), client.Continue(continueToken)); err != nil {
			return sent, err
		}
		for _, obj := range k.items(list) {
			ev := event.GenericEvent{Object: obj}
			if k.supervised {
				select {
				case k.ch <- ev:
					sent++
				default: // controller mid-rebuild; its initial list covers this
				}
				continue
			}
			select {
			case k.ch <- ev:
				sent++
			case <-ctx.Done():
				return sent, ctx.Err()
			}
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return sent, nil
		}
	}
}
