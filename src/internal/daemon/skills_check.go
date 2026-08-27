package daemon

import (
	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/skills"
)

// warnUnresolvedSkills logs one warning per declared skill that resolves to no
// file on disk, naming the agent, the skill and every candidate path tried.
//
// `apiary validate` already rejects such a config, but a daemon can be started
// on a hive whose skills were renamed or deleted after the config was written.
// Without this the run proceeds with the skill simply absent from the agent's
// context and nothing anywhere says so (issue #429).
func (d *Dispatcher) warnUnresolvedSkills() {
	if d.cfg == nil {
		return
	}
	for _, ac := range d.cfg.Agents {
		for _, name := range ac.Skills {
			if name == "" {
				continue
			}
			if res := skills.Resolve("", name); !res.Found() {
				aplog.Warn("agent %s: %s", ac.ID, res.Reason())
			}
		}
	}
}
