package state

import (
	"fmt"
	"sort"
)

// DAG represents a directed acyclic graph of state dependencies.
// It uses Kahn's algorithm for topological sorting and groups
// independent nodes into parallel execution levels.
type DAG struct {
	nodes map[string]State
	edges map[string][]string // node -> list of dependencies (all requisite types)
}

// NewDAG creates a DAG from a list of states.
// Each state's Reqs().AllDeps() defines edges to its dependencies.
// All requisite types (require, watch, onchanges, onfail) create ordering edges.
func NewDAG(states []State) (*DAG, error) {
	d := &DAG{
		nodes: make(map[string]State, len(states)),
		edges: make(map[string][]string, len(states)),
	}

	for _, s := range states {
		name := s.Name()
		if _, exists := d.nodes[name]; exists {
			return nil, fmt.Errorf("dag: duplicate state %q", name)
		}
		d.nodes[name] = s
		d.edges[name] = s.Reqs().AllDeps()
	}

	// Validate that all dependencies reference existing states.
	for name, deps := range d.edges {
		for _, dep := range deps {
			if _, ok := d.nodes[dep]; !ok {
				return nil, fmt.Errorf("dag: state %q requires unknown state %q", name, dep)
			}
		}
	}

	return d, nil
}

// Level represents a set of states that can execute in parallel.
// All states in a level have their dependencies satisfied by prior levels.
type Level struct {
	States []State
}

// Resolve performs a topological sort using Kahn's algorithm and groups
// the result into parallel execution levels. Returns an error if
// a cycle is detected.
func (d *DAG) Resolve() ([]Level, error) {
	// Calculate in-degree for each node.
	inDegree := make(map[string]int, len(d.nodes))
	// Reverse edges: dependency -> list of dependents.
	dependents := make(map[string][]string, len(d.nodes))

	for name := range d.nodes {
		inDegree[name] = 0
	}
	for name, deps := range d.edges {
		inDegree[name] = len(deps)
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}

	// Seed the queue with all nodes that have no dependencies.
	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue) // deterministic ordering within a level

	var levels []Level
	processed := 0

	for len(queue) > 0 {
		// All nodes in the current queue form one parallel level.
		level := Level{States: make([]State, 0, len(queue))}
		for _, name := range queue {
			level.States = append(level.States, d.nodes[name])
		}
		levels = append(levels, level)
		processed += len(queue)

		// Find the next set of nodes whose dependencies are now satisfied.
		var next []string
		for _, name := range queue {
			for _, dep := range dependents[name] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		sort.Strings(next)
		queue = next
	}

	if processed != len(d.nodes) {
		return nil, fmt.Errorf("dag: cycle detected, resolved %d of %d states", processed, len(d.nodes))
	}

	return levels, nil
}

// Order returns a flat topological ordering of all states.
func (d *DAG) Order() ([]State, error) {
	levels, err := d.Resolve()
	if err != nil {
		return nil, err
	}

	result := make([]State, 0, len(d.nodes))
	for _, level := range levels {
		result = append(result, level.States...)
	}
	return result, nil
}

// Get returns the state with the given name, or nil if not found.
func (d *DAG) Get(name string) State {
	return d.nodes[name]
}

// Len returns the number of states in the DAG.
func (d *DAG) Len() int {
	return len(d.nodes)
}
