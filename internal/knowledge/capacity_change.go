package knowledge

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type capacityCost struct {
	labels, except trace.Labels
	before, after  int
	count          int
}

type capacityCosts []capacityCost

// An exception cannot grant credit for an item that run never received
func (s Set) capacityGrowth(before Set, run RunScope) Set {
	costs := capacityCosts{}
	for _, k := range s.runCarried("", run) {
		costs = costs.add(capacityCost{labels: k.Run.Labels, after: utf8.RuneCountInString(k.Text()), count: 1})
	}
	for _, k := range before.runCarried("", run) {
		costs = costs.add(capacityCost{labels: k.Run.Labels, except: k.Run.Except,
			before: utf8.RuneCountInString(k.Text())})
	}
	labels := costs.growth(run.Labels)
	if labels == nil {
		return nil
	}
	out := Set{}
	for _, k := range s {
		scope := *k.Run
		scope.Except = nil
		if k.Veto == nil && scope.Admits(run.Producer, labels) {
			out = append(out, k)
		}
	}
	return out
}

// Equal conditions cancel unchanged costs before label branching
func (costs capacityCosts) add(cost capacityCost) capacityCosts {
	for i, old := range costs {
		if maps.EqualFunc(old.labels, cost.labels, slices.Equal[[]string]) &&
			maps.EqualFunc(old.except, cost.except, slices.Equal[[]string]) {
			costs[i].before += cost.before
			costs[i].after += cost.after
			costs[i].count += cost.count
			return costs
		}
	}
	return append(costs, cost)
}

func (costs capacityCosts) growth(required trace.Labels) trace.Labels {
	domains := costs.domains(required)
	keys := slices.Sorted(maps.Keys(domains))
	labels := trace.Labels{}
	var best trace.Labels
	bestChars, bestCount := 0, 0
	var search func(capacityCosts, int)
	search = func(rest capacityCosts, depth int) {
		upper, growth, count := rest.bounds(keys[depth:])
		if upper <= RunChars || growth <= 0 || upper < bestChars ||
			upper == bestChars && count <= bestCount {
			return
		}
		if depth == len(keys) {
			best, bestChars, bestCount = maps.Clone(labels), upper, count
			return
		}
		key := keys[depth]
		for _, value := range domains[key] {
			delete(labels, key)
			if value != "" {
				labels[key] = []string{value}
			}
			search(rest.matching(key, value), depth+1)
		}
		delete(labels, key)
	}
	search(costs, 0)
	return best
}

func (costs capacityCosts) domains(required trace.Labels) trace.Labels {
	domains := maps.Clone(required)
	if domains == nil {
		domains = trace.Labels{}
	}
	for _, cost := range costs {
		for _, labels := range []trace.Labels{cost.labels, cost.except} {
			for key, values := range labels {
				if _, bound := required[key]; !bound {
					domains[key] = append(domains[key], append([]string{""}, values...)...)
				}
			}
		}
	}
	for key, values := range domains {
		values = slices.Clone(values)
		slices.Sort(values)
		domains[key] = slices.Compact(values)
		weights := map[string]int{}
		for _, value := range domains[key] {
			for _, cost := range costs {
				if cost.admits(key, value) {
					weights[value] += cost.after
				}
			}
		}
		slices.SortFunc(domains[key], func(a, b string) int {
			return cmp.Or(cmp.Compare(weights[b], weights[a]), strings.Compare(a, b))
		})
	}
	return domains
}

func (costs capacityCosts) bounds(remaining []string) (upper, growth, count int) {
	for _, cost := range costs {
		upper += cost.after
		count += cost.count
		unresolved := slices.ContainsFunc(remaining, func(key string) bool {
			_, required := cost.labels[key]
			return required || len(cost.except[key]) != 0
		})
		delta := cost.after - cost.before
		if unresolved {
			delta = max(0, delta)
		}
		growth += delta
	}
	return upper, growth, count
}

func (costs capacityCosts) matching(key, value string) capacityCosts {
	out := capacityCosts{}
	for _, cost := range costs {
		if cost.admits(key, value) {
			out = append(out, cost)
		}
	}
	return out
}

func (cost capacityCost) admits(key, value string) bool {
	values, required := cost.labels[key]
	return (!required || slices.Contains(values, value)) && !slices.Contains(cost.except[key], value)
}
