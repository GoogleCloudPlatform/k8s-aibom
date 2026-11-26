package controller

import (
	"testing"

	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/watchlist"
)

// The discovery informer and the InferencePool reflector must never open
// a watch with sendInitialEvents: API servers without the WatchList gate
// reject it, and the rejection used to be reported as a discovery outage
// at every start (one spurious WatchUnhealthy, Degraded, WatchRecovered).
// client-go's opt-out is a client that declares the semantics
// unsupported; this pins that the marker the supervisor passes is read
// as such.
func TestListWatchOnly_DisablesWatchListSemantics(t *testing.T) {
	lw := toolscache.ToListWatcherWithWatchListSemantics(&toolscache.ListWatch{}, listWatchOnly{})
	if !watchlist.DoesClientNotSupportWatchListSemantics(lw) {
		t.Fatal("listWatchOnly must make the ListerWatcher report WatchList semantics as unsupported")
	}
	if watchlist.DoesClientNotSupportWatchListSemantics(toolscache.ToListWatcherWithWatchListSemantics(&toolscache.ListWatch{}, struct{}{})) {
		t.Fatal("control: a plain client must not report WatchList semantics as unsupported")
	}
}
