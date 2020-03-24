package kraph

import (
	"fmt"
	"strings"

	"github.com/milosgajdos/kraph/api"
	"github.com/milosgajdos/kraph/query"
	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/encoding"
	"gonum.org/v1/gonum/graph/encoding/dot"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/traverse"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	// DefaultWeight is default edge weight
	DefaultWeight = 0.0
)

// Kraph is a graph of Kubernetes resources
type Kraph struct {
	*simple.WeightedUndirectedGraph
	// client discovers and maps APIs
	client api.Client
	// options
	opts Options
	// Global DOT attributes
	GraphAttrs Attrs
	NodeAttrs  Attrs
	EdgeAttrs  Attrs
}

// New creates new Kraph with given options and returns it
// It never returns error, but it might in the future
func New(client api.Client, opts ...Option) (*Kraph, error) {
	kraphOpts := Options{}
	for _, apply := range opts {
		apply(&kraphOpts)
	}

	return &Kraph{
		WeightedUndirectedGraph: simple.NewWeightedUndirectedGraph(0.0, 0.0),
		client:                  client,
		opts:                    kraphOpts,
		GraphAttrs:              make(Attrs),
		NodeAttrs:               make(Attrs),
		EdgeAttrs:               make(Attrs),
	}, nil
}

// Options returns kraph options
func (k *Kraph) Options() Options {
	return k.opts
}

// NewNode creates new kraph node adds it to the graph and returns it.
func (k *Kraph) NewNode(obj api.Object, opts ...NodeOption) *Node {
	nodeOpts := newNodeOptions(opts...)

	n := &Node{
		Attrs:    nodeOpts.Attrs,
		id:       k.WeightedUndirectedGraph.NewNode().ID(),
		name:     obj.Kind() + "-" + obj.Name(),
		metadata: nodeOpts.Metadata,
	}

	for _, attr := range nodeOpts.Attrs.Attributes() {
		n.SetAttribute(attr.Key, attr.Value)
	}

	n.metadata["object"] = obj

	k.AddNode(n)

	return n
}

// NewEdge adds a new edge between from node to to node to the graph
// or returns an existing edge if it already exists in the graph.
// It will panic if the IDs of the from and to nodes are the same.
func (k *Kraph) NewEdge(from, to graph.Node, opts ...EdgeOption) *Edge {
	if e := k.Edge(from.ID(), to.ID()); e != nil {
		return e.(*Edge)
	}

	edgeOpts := newEdgeOptions(opts...)

	e := &Edge{
		Attrs:    edgeOpts.Attrs,
		from:     from.(*Node),
		to:       to.(*Node),
		weight:   edgeOpts.Weight,
		metadata: edgeOpts.Metadata,
	}

	for _, attr := range edgeOpts.Attrs.Attributes() {
		e.SetAttribute(attr.Key, attr.Value)
	}

	k.SetWeightedEdge(e)

	return e
}

// DOTID returns the graph's DOT ID.
func (k *Kraph) DOTID() string {
	return "kraph"
}

// DOTAttributers returns the global DOT kraph attributers
func (k *Kraph) DOTAttributers() (graph, node, edge encoding.Attributer) {
	return k.GraphAttrs, k.NodeAttrs, k.EdgeAttrs
}

// DOT returns the GrapViz dot representation of kraph
func (k *Kraph) DOT() (string, error) {
	b, err := dot.Marshal(k, "", "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode kraph into dot: %v", err)
	}

	return string(b), nil
}

// linkObject links obj to all of its neighbours
func (k *Kraph) linkObjects(obj api.Object, neighbs []api.Object) {
	from := k.NewNode(obj)

	for _, o := range neighbs {
		to := k.NewNode(o)
		if e := k.Edge(from.ID(), to.ID()); e == nil {
			// TODO: this feel s a bit out of place
			opts := newEdgeOptions()
			opts.Attrs["relation"] = "isOwned"
			e = k.NewEdge(from, to, EdgeAttrs(opts.Attrs))
		}
	}
}

// buildGraph builds a graph from given topology and returns it
func (k *Kraph) buildGraph(top api.Top) (graph.Graph, error) {
	switch r := top.Raw().(type) {
	// TODO: make this less hacky
	// One of the options is getting all objects
	// and then by iterating over them querying
	// the topology one by one when building the graph
	case map[string]map[string]map[string]api.Object:
		for _, kinds := range r {
			for _, names := range kinds {
				for _, obj := range names {
					raw := obj.Raw().(unstructured.Unstructured)
					var neighbs []api.Object
					for _, owner := range raw.GetOwnerReferences() {
						queryOpts := []query.Option{
							query.Kind(strings.ToLower(owner.Kind)),
							query.Name(strings.ToLower(owner.Name)),
						}
						objs, err := top.Get(queryOpts...)
						if err != nil {
							return nil, err
						}
						neighbs = append(neighbs, objs...)
					}
					k.linkObjects(obj, neighbs)
				}
			}
		}
	default:
		return nil, ErrUnknownTop
	}

	return k.WeightedUndirectedGraph, nil
}

// Build builds resource graph and returns it
func (k *Kraph) Build() (graph.Graph, error) {
	api, err := k.client.Discover()
	if err != nil {
		return nil, fmt.Errorf("failed discovering API: %w", err)
	}

	top, err := k.client.Map(api)
	if err != nil {
		return nil, fmt.Errorf("failed mapping API: %w", err)
	}

	return k.buildGraph(top)
}

// Query allows to query a kraph node and returns it
func (k *Kraph) QueryNode(q ...query.Option) (*Node, error) {
	return nil, ErrNotImplemented
}

// SubGraph returns a subgraph of node n up to given depth.
// It performs a Breadth First Search (BFS) and creates a subgraph
// from the nodes traversed during the search.
func (k *Kraph) SubGraph(n *Node, depth int) (graph.Graph, error) {
	g := simple.NewWeightedUndirectedGraph(0.0, 0.0)

	bfs := traverse.BreadthFirst{}

	// TODO: need to maintain a map of krap.Node.ID -> g.Node.ID
	// so we know how to wire the nodes of the new subgraph together
	_ = bfs.Walk(k, n, func(n graph.Node, d int) bool {
		if d > depth {
			return true
		}
		// TODO: we should move this into bfs.Visit(graph.Node)
		// i.e. add Visit field into BFS struct
		// https://godoc.org/gonum.org/v1/gonum/graph/traverse#BreadthFirst
		// TODO: make a deep copy of n
		// and add it to the graph g
		return false
	})

	return g, nil
}

// GetNodesWithAttr returns a slice of nodes with the given attribute set
// If it does not find any matching nodes it returns an empty slice.
func (k *Kraph) GetNodesWithAttr(attr encoding.Attribute) ([]*Node, error) {
	var nodes []*Node

	found := false
	for _, node := range graph.NodesOf(k.Nodes()) {
		n := node.(*Node)
		if val := n.Get(attr.Key); val != "" {
			// attribute key exists; check its value
			switch attr.Value {
			case "*":
				found = true
			case val:
				found = true
			default:
				// continue
			}
		}
		if found {
			nodes = append(nodes, n)
			found = false
		}
	}

	return nodes, nil
}

// GetEdgesWithAttr returns a slice of Edges with the given attribute
// If it does not find any matching edges it returns empty slice.
func (k *Kraph) GetEdgesWithAttr(attr encoding.Attribute) ([]*Edge, error) {
	var edges []*Edge

	found := false
	for _, edge := range graph.EdgesOf(k.Edges()) {
		e := edge.(*Edge)
		if val := e.Get(attr.Key); val != "" {
			// attribute key exists; check its value
			switch attr.Value {
			case "*":
				found = true
			case val:
				found = true
			default:
				// continue
			}
		}
		if found {
			edges = append(edges, e)
			found = false
		}
	}

	return edges, nil
}
