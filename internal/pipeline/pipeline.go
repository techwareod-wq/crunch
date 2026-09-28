package pipeline

import "context"

// ProcessType identifies an async processing step. Each service defines its own constants.
type ProcessType string

// Dispatcher enqueues async work onto the message queue.
// Mirrors interfaces.Dispatcher to avoid circular imports.
type Dispatcher interface {
	Dispatch(ctx context.Context, processType string, userID string, payload any) error
}

// DispatchContext carries the common identity fields a step passes to DispatchNext.
type DispatchContext struct {
	UserID             string
	WebEntityID        string
	WebEntityContextID string
}

// DispatchFunc overrides the default dispatch behavior for an edge.
// It receives the dispatcher so it can send one or many messages with any payload shape.
// Returning nil means the edge was handled (even if zero messages were dispatched).
type DispatchFunc func(ctx context.Context, d Dispatcher, next ProcessType, dc DispatchContext) error

// Stage is a node in the DAG. Exists for build-time validation and introspection.
type Stage struct {
	ProcessType ProcessType
	Label       string
}

// Edge is a directed connection between two stages.
type Edge struct {
	From       ProcessType
	To         ProcessType
	DispatchFn DispatchFunc // nil = standard payload dispatch
}

// StandardPayload is the default payload dispatched when an edge has no DispatchFunc.
type StandardPayload struct {
	WebEntityID        string `json:"webEntityId"`
	WebEntityContextID string `json:"webEntityContextId"`
	CompetitorURL      string `json:"competitorUrl,omitempty"`
}

// Pipeline is a DAG of processing stages. Constructed once at service startup via Builder.
// Immutable and safe for concurrent use after Build().
type Pipeline struct {
	dispatcher Dispatcher
	outgoing   map[ProcessType][]Edge
	incoming   map[ProcessType][]Edge
	stages     map[ProcessType]Stage
}

// DispatchNext resolves all outgoing edges from currentStep and dispatches them.
//
// For edges with a DispatchFunc, the function is called with full control over dispatch.
// For edges without a DispatchFunc, a StandardPayload is dispatched to the target stage.
//
// Returns nil for terminal stages (no outgoing edges).
func (p *Pipeline) DispatchNext(ctx context.Context, currentStep ProcessType, dc DispatchContext) error {
	edges, ok := p.outgoing[currentStep]
	if !ok {
		return nil
	}

	for _, edge := range edges {
		if edge.DispatchFn != nil {
			if err := edge.DispatchFn(ctx, p.dispatcher, edge.To, dc); err != nil {
				return err
			}
			continue
		}

		payload := StandardPayload{
			WebEntityID:        dc.WebEntityID,
			WebEntityContextID: dc.WebEntityContextID,
		}
		if err := p.dispatcher.Dispatch(ctx, string(edge.To), dc.UserID, payload); err != nil {
			return err
		}
	}

	return nil
}

// Successors returns the process types reachable in one hop from the given step.
func (p *Pipeline) Successors(step ProcessType) []ProcessType {
	edges := p.outgoing[step]
	out := make([]ProcessType, len(edges))
	for i, e := range edges {
		out[i] = e.To
	}
	return out
}

// Predecessors returns the process types that have an edge into the given step.
func (p *Pipeline) Predecessors(step ProcessType) []ProcessType {
	edges := p.incoming[step]
	out := make([]ProcessType, len(edges))
	for i, e := range edges {
		out[i] = e.From
	}
	return out
}
