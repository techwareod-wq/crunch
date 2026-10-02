package domain

import (
	"context"
	"sort"
	"strings"
)

// Snapshot is an immutable view of the attribute tree plus the industries at
// one rulesVersion. Built once per reload and shared read-only; never mutate
// a snapshot or the slices it returns (Clone a node before editing it).
type Snapshot struct {
	// Version is the rulesVersion the snapshot was loaded at.
	Version int64
	// Nodes is every node in tree order: depth-first from the root, siblings
	// by (order, key); each node's fields sorted by (order, key). Nodes not
	// reachable from the root (a damaged tree) come last.
	Nodes []Node
	// Industries is every industry by (order, key).
	Industries []Industry

	byKey    map[string]*Node
	children map[string][]string // parentKey → child keys in order
}

// NewSnapshot builds a snapshot from raw docs.
func NewSnapshot(version int64, nodes []Node, industries []Industry) *Snapshot {
	s := &Snapshot{
		Version:  version,
		byKey:    make(map[string]*Node, len(nodes)),
		children: map[string][]string{},
	}
	raw := make(map[string]Node, len(nodes))
	sorted := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		n = n.Clone()
		sort.SliceStable(n.Fields, func(i, j int) bool {
			if n.Fields[i].Order != n.Fields[j].Order {
				return n.Fields[i].Order < n.Fields[j].Order
			}
			return n.Fields[i].Key < n.Fields[j].Key
		})
		raw[n.Key] = n
		sorted = append(sorted, n)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Order != sorted[j].Order {
			return sorted[i].Order < sorted[j].Order
		}
		return sorted[i].Key < sorted[j].Key
	})
	for _, n := range sorted {
		if n.ParentKey != "" {
			s.children[n.ParentKey] = append(s.children[n.ParentKey], n.Key)
		}
	}

	seen := make(map[string]bool, len(nodes))
	var walk func(key string)
	walk = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		s.Nodes = append(s.Nodes, raw[key])
		for _, c := range s.children[key] {
			walk(c)
		}
	}
	if _, ok := raw[RootKey]; ok {
		walk(RootKey)
	}
	// Orphans and cycle members are unreachable; keep them so nothing
	// silently disappears (the evaluator treats them as "no").
	for _, n := range sorted {
		walk(n.Key)
	}
	for i := range s.Nodes {
		s.byKey[s.Nodes[i].Key] = &s.Nodes[i]
	}

	s.Industries = make([]Industry, len(industries))
	for i, ind := range industries {
		s.Industries[i] = ind.Clone()
	}
	sort.SliceStable(s.Industries, func(i, j int) bool {
		if s.Industries[i].Order != s.Industries[j].Order {
			return s.Industries[i].Order < s.Industries[j].Order
		}
		return s.Industries[i].Key < s.Industries[j].Key
	})
	return s
}

// EmptySnapshot is the snapshot before the first load.
func EmptySnapshot() *Snapshot { return NewSnapshot(0, nil, nil) }

// Node looks up a node by key.
func (s *Snapshot) Node(key string) (*Node, bool) {
	n, ok := s.byKey[key]
	return n, ok
}

// Field resolves a full field path "<node>.<field>".
func (s *Snapshot) Field(path string) (*Node, *Field, bool) {
	nk, fk, ok := strings.Cut(path, ".")
	if !ok {
		return nil, nil, false
	}
	n, ok := s.byKey[nk]
	if !ok {
		return nil, nil, false
	}
	f, ok := n.Field(fk)
	if !ok {
		return nil, nil, false
	}
	return n, f, true
}

// Children returns parentKey's child keys in order.
func (s *Snapshot) Children(parentKey string) []string {
	return s.children[parentKey]
}

// Descendants returns every node below key, depth-first.
func (s *Snapshot) Descendants(key string) []string {
	var out []string
	seen := map[string]bool{key: true}
	var walk func(k string)
	walk = func(k string) {
		for _, c := range s.children[k] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			walk(c)
		}
	}
	walk(key)
	return out
}

// IsAncestor reports whether anc is a strict ancestor of key.
func (s *Snapshot) IsAncestor(anc, key string) bool {
	seen := map[string]bool{}
	for n, ok := s.Node(key); ok && n.ParentKey != "" && !seen[n.Key]; n, ok = s.Node(n.ParentKey) {
		seen[n.Key] = true
		if n.ParentKey == anc {
			return true
		}
	}
	return false
}

// Industry looks up an industry by key.
func (s *Snapshot) Industry(key string) (*Industry, bool) {
	for i := range s.Industries {
		if s.Industries[i].Key == key {
			return &s.Industries[i], true
		}
	}
	return nil, false
}

// TreeNode is one node of the nested admin tree view.
type TreeNode struct {
	Node
	Children []TreeNode `json:"children"`
}

// Tree returns the nested tree from the root (empty before the bootstrap).
func (s *Snapshot) Tree() []TreeNode {
	var build func(key string) TreeNode
	build = func(key string) TreeNode {
		t := TreeNode{Node: *s.byKey[key], Children: []TreeNode{}}
		for _, c := range s.children[key] {
			t.Children = append(t.Children, build(c))
		}
		return t
	}
	if _, ok := s.byKey[RootKey]; !ok {
		return []TreeNode{}
	}
	return []TreeNode{build(RootKey)}
}

// Rules gives other modules the current attribute snapshot (implemented by
// the attributes module's cache, wired in cmd/service/modules.go).
type Rules interface {
	// Snapshot returns the current snapshot; never nil.
	Snapshot() *Snapshot
	// SnapshotAtLeast returns a snapshot at version ≥ v, reloading from the
	// DB when the cached one is older.
	SnapshotAtLeast(ctx context.Context, v int64) (*Snapshot, error)
}
