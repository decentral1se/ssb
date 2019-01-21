package graph

import (
	"fmt"
	"math"

	"github.com/go-kit/kit/log"
	"github.com/pkg/errors"
	"go.cryptoscope.co/ssb"
)

type authorizer struct {
	b       *builder
	from    *ssb.FeedRef
	maxHops int
	log     log.Logger
}

// ErrNoSuchFrom should only happen if you reconstruct your existing log from the network
type ErrNoSuchFrom struct{ *ssb.FeedRef }

func (nsf ErrNoSuchFrom) Error() string { return fmt.Sprintf("ssb/graph: no such from: %s", nsf.Ref()) }

func (a *authorizer) Authorize(to *ssb.FeedRef) error {
	fg, err := a.b.Build()
	if err != nil {
		return errors.Wrap(err, "graph/Authorize: failed to make friendgraph")
	}

	if fg.Nodes() == 0 {
		a.log.Log("event", "warning:authbypass", "msg", "trust on first use")
		return nil
	}

	if fg.Follows(a.from, to) {
		a.log.Log("debug", "following") //, "ref", to.Ref())
		return nil
	}

	// TODO we need to check that `from` is in the graph, instead of checking if it's empty
	// only important in the _resync existing feed_ case. should maybe not construct this authorizer then?
	var distLookup *Lookup
	distLookup, err = fg.MakeDijkstra(a.from)
	if err != nil {
		// for now adding this as a kludge so that stuff works when you don't get your own feed during initial re-sync
		// if it's a new key there should be follows quickly anyway and this shouldn't happen then.... yikes :'(
		if _, ok := err.(*ErrNoSuchFrom); ok {
			return nil
		}
		return errors.Wrap(err, "graph/Authorize: failed to construct dijkstra")
	}

	// dist includes start and end of the path so Alice to Bob will be
	// p:=[Alice, some, friends, Bob]
	// len(p) == 4
	p, d := distLookup.Dist(to)
	a.log.Log("debug", "dist", "d", d, "p", fmt.Sprintf("%v", p))
	if math.IsInf(d, -1) || math.IsInf(d, 1) || int(d) > a.maxHops {
		// d == -Inf > peer not connected to the graph
		// d == +Inf > peer directly(?) blocked
		return &ssb.ErrOutOfReach{Dist: int(d), Max: a.maxHops}
	}

	return nil

}
