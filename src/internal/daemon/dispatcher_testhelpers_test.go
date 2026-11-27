package daemon

import "github.com/orlandoburli/apiary/internal/config"

// dispatcherWithConfig stores cfg in d before returning it. atomic.Pointer
// cannot be assigned from a composite literal, and tests build Dispatchers with
// just the fields they need, so every literal that carried a cfg field goes
// through this helper instead.
func dispatcherWithConfig(cfg *config.Config, d *Dispatcher) *Dispatcher {
	d.cfgPtr.Store(cfg)
	return d
}
