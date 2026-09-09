package main

import (
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/grove-sh/cli/internal/daemon"
)

// Whether anything would actually notice the route going away. A detached lease
// nothing answers on is a stopped stack keeping its ports, which costs nothing
// to drop; one whose port answers is a live service, the same as an attached
// lease, and its working URL stops working.
func servingLeases(held []daemon.Live) []daemon.Live {
	var live []daemon.Live
	for _, lease := range held {
		if !lease.Detached || answering(lease.Port) {
			live = append(live, lease)
		}
	}
	return live
}

// Only the recovery differs: grove hold puts a detached route back on its own,
// while an attached lease belongs to a command with nothing to reconnect to.
func attachedLeases(held []daemon.Live) []daemon.Live {
	var live []daemon.Live
	for _, lease := range held {
		if !lease.Detached {
			live = append(live, lease)
		}
	}
	return live
}

// Per context rather than per lease: one command holds one for each entry it
// declares, and someone deciding whether to interrupt wants the project.
func heldBy(leases []daemon.Live) string {
	worktrees := map[string]string{}
	var slugs []string
	for _, lease := range leases {
		if _, seen := worktrees[lease.Slug]; !seen {
			slugs = append(slugs, lease.Slug)
		}
		worktrees[lease.Slug] = lease.Worktree
	}
	slices.Sort(slugs)

	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 0, 3, ' ', 0)
	for _, slug := range slugs {
		fmt.Fprintf(w, "    %s\t%s\n", slug, worktrees[slug])
	}
	w.Flush()
	return out.String()
}

func refuseToStop(held []daemon.Live) string {
	live := servingLeases(held)
	if len(live) == 0 {
		return ""
	}
	grove := invocation()
	if len(attachedLeases(live)) == 0 {
		return fmt.Sprintf(`Something is still serving in each worktree listed:

%s
Stopping takes those routes with the daemon, and '%s hold' is what puts them
back. Stop anyway with '%s stop --force'.`, heldBy(live), grove, grove)
	}
	return fmt.Sprintf(`Something is still serving in each worktree listed:

%s
Stopping takes those routes with the daemon. '%s hold' puts the detached ones
back, but a lease held by a running command needs that command run again.
Stop anyway with '%s stop --force'.`, heldBy(live), grove, grove)
}

// Uninstall stops grove, so it costs what stop costs, plus a root the machine
// no longer accepts under anything still answering on those hostnames.
func refuseToUninstall(held []daemon.Live) string {
	live := servingLeases(held)
	if len(live) == 0 {
		return ""
	}
	return fmt.Sprintf(`Something is still serving in each worktree listed:

%s
Uninstalling stops grove and untrusts its root, so those routes go away and
nothing answering on them stays trusted. Uninstall anyway with
'%s uninstall --force'.`,
		heldBy(live), invocation())
}
