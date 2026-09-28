package pipeline

import "fmt"

// Builder constructs an immutable Pipeline using a fluent API.
// Not safe for concurrent use — build the pipeline once at startup.
type Builder struct {
	dispatcher Dispatcher
	stages     map[ProcessType]Stage
	edges      []Edge
}

// NewBuilder creates a Builder wired to the given Dispatcher.
func NewBuilder(dispatcher Dispatcher) *Builder {
	return &Builder{
		dispatcher: dispatcher,
		stages:     make(map[ProcessType]Stage),
	}
}

// AddStage registers a node in the DAG.
func (b *Builder) AddStage(stage Stage) *Builder {
	b.stages[stage.ProcessType] = stage
	return b
}

// AddEdge registers a directed edge between two stages.
// Pass a nil dispatchFn for standard payload dispatch.
func (b *Builder) AddEdge(from, to ProcessType, dispatchFn DispatchFunc) *Builder {
	b.edges = append(b.edges, Edge{
		From:       from,
		To:         to,
		DispatchFn: dispatchFn,
	})
	return b
}

// Build validates the DAG and returns an immutable Pipeline.
func (b *Builder) Build() (*Pipeline, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}

	outgoing := make(map[ProcessType][]Edge)
	incoming := make(map[ProcessType][]Edge)

	for _, edge := range b.edges {
		outgoing[edge.From] = append(outgoing[edge.From], edge)
		incoming[edge.To] = append(incoming[edge.To], edge)
	}

	return &Pipeline{
		dispatcher: b.dispatcher,
		outgoing:   outgoing,
		incoming:   incoming,
		stages:     b.stages,
	}, nil
}

func (b *Builder) validate() error {
	// All edges must reference registered stages
	for _, edge := range b.edges {
		if _, ok := b.stages[edge.From]; !ok {
			return fmt.Errorf("pipeline: edge references unregistered stage %q (from)", edge.From)
		}
		if _, ok := b.stages[edge.To]; !ok {
			return fmt.Errorf("pipeline: edge references unregistered stage %q (to)", edge.To)
		}
	}

	// No duplicate edges (same From + To)
	seen := make(map[[2]ProcessType]struct{})
	for _, edge := range b.edges {
		key := [2]ProcessType{edge.From, edge.To}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("pipeline: duplicate edge from %q to %q", edge.From, edge.To)
		}
		seen[key] = struct{}{}
	}

	// Cycle detection via DFS
	if err := b.detectCycles(); err != nil {
		return err
	}

	return nil
}

func (b *Builder) detectCycles() error {
	adj := make(map[ProcessType][]ProcessType)
	for _, edge := range b.edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
	}

	const (
		white = 0 // unvisited
		gray  = 1 // in current DFS path
		black = 2 // fully processed
	)

	color := make(map[ProcessType]int)
	for pt := range b.stages {
		color[pt] = white
	}

	var dfs func(node ProcessType) error
	dfs = func(node ProcessType) error {
		color[node] = gray
		for _, next := range adj[node] {
			switch color[next] {
			case gray:
				return fmt.Errorf("pipeline: cycle detected involving %q -> %q", node, next)
			case white:
				if err := dfs(next); err != nil {
					return err
				}
			}
		}
		color[node] = black
		return nil
	}

	for pt := range b.stages {
		if color[pt] == white {
			if err := dfs(pt); err != nil {
				return err
			}
		}
	}

	return nil
}
