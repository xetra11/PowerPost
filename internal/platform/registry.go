package platform

import "slices"

// Registry holds all known adapters in display order.
type Registry struct {
	adapters []Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	return &Registry{adapters: adapters}
}

func (r *Registry) Get(id ID) (Adapter, bool) {
	i := slices.IndexFunc(r.adapters, func(a Adapter) bool { return a.ID() == id })
	if i < 0 {
		return nil, false
	}
	return r.adapters[i], true
}

func (r *Registry) All() []Adapter {
	return slices.Clone(r.adapters)
}
