package domain

import (
	"context"
	"sort"
)

// Snapshot is an immutable view of the attribute tree plus the industries at
// one rulesVersion. Built once per reload and shared read-only; never mutate
// a snapshot or the slices it returns.
type Snapshot struct {
	// Version is the rulesVersion the snapshot was loaded at.
	Version int64
	// Defs is every node (retired included) in tree order: depth-first,
	// siblings by (order, key).
	Defs []AttrDef
	// Industries is every industry (retired included) by (order, key).
	Industries []Industry

	byKey    map[string]*AttrDef
	children map[string][]string // parentKey ("" = root) → child keys in order
}

// NewSnapshot builds a snapshot from raw docs. Nodes whose parent is missing
// are treated as roots so a damaged tree still evaluates.
func NewSnapshot(version int64, defs []AttrDef, industries []Industry) *Snapshot {
	s := &Snapshot{
		Version:  version,
		byKey:    make(map[string]*AttrDef, len(defs)),
		children: map[string][]string{},
	}
	raw := make(map[string]AttrDef, len(defs))
	for _, d := range defs {
		raw[d.Key] = d
	}
	sorted := make([]AttrDef, len(defs))
	copy(sorted, defs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Order != sorted[j].Order {
			return sorted[i].Order < sorted[j].Order
		}
		return sorted[i].Key < sorted[j].Key
	})
	for _, d := range sorted {
		p := d.ParentKey
		if _, ok := raw[p]; !ok || p == d.Key {
			p = ""
		}
		s.children[p] = append(s.children[p], d.Key)
	}

	seen := make(map[string]bool, len(defs))
	var walk func(key string)
	walk = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		s.Defs = append(s.Defs, raw[key])
		for _, c := range s.children[key] {
			walk(c)
		}
	}
	for _, k := range s.children[""] {
		walk(k)
	}
	// Cycle members are unreachable from the roots; keep them so nothing
	// silently disappears.
	for _, d := range sorted {
		walk(d.Key)
	}
	for i := range s.Defs {
		s.byKey[s.Defs[i].Key] = &s.Defs[i]
	}

	s.Industries = make([]Industry, len(industries))
	copy(s.Industries, industries)
	sort.SliceStable(s.Industries, func(i, j int) bool {
		if s.Industries[i].Order != s.Industries[j].Order {
			return s.Industries[i].Order < s.Industries[j].Order
		}
		return s.Industries[i].Key < s.Industries[j].Key
	})
	return s
}

// EmptySnapshot is the snapshot before anything is seeded.
func EmptySnapshot() *Snapshot { return NewSnapshot(0, nil, nil) }

// Def looks up a node by key.
func (s *Snapshot) Def(key string) (*AttrDef, bool) {
	d, ok := s.byKey[key]
	return d, ok
}

// Children returns parentKey's child keys in order ("" = roots).
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
	for d, ok := s.Def(key); ok && d.ParentKey != "" && !seen[d.Key]; d, ok = s.Def(d.ParentKey) {
		seen[d.Key] = true
		if d.ParentKey == anc {
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
	AttrDef
	Children []TreeNode `json:"children"`
}

// Tree returns the nested tree (retired nodes included, flagged).
func (s *Snapshot) Tree() []TreeNode {
	var build func(parent string) []TreeNode
	build = func(parent string) []TreeNode {
		out := []TreeNode{}
		for _, k := range s.children[parent] {
			d := s.byKey[k]
			out = append(out, TreeNode{AttrDef: *d, Children: build(k)})
		}
		return out
	}
	return build("")
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
