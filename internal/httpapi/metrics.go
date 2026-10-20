package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
)

func writeStateMetrics(w http.ResponseWriter, name, help, label string, states map[string]int) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	keys := make([]string, 0, len(states))
	for state := range states {
		keys = append(keys, state)
	}
	sort.Strings(keys)
	for _, state := range keys {
		fmt.Fprintf(w, "%s{%s=%s} %d\n", name, label, strconv.Quote(state), states[state])
	}
}

func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	ready := 1
	if a.Ready != nil {
		if err := a.Ready(r.Context()); err != nil {
			ready = 0
		}
	}
	snapshot, err := a.Store.Metrics(r.Context(), a.TenantID)
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "# HELP bots_up Whether this Bots process is serving metrics.\n# TYPE bots_up gauge\nbots_up 1\n")
	fmt.Fprintf(w, "# HELP bots_ready Whether readiness dependencies are currently healthy.\n# TYPE bots_ready gauge\nbots_ready %d\n", ready)
	writeStateMetrics(w, "bots_inbox_total", "Current inbox rows by state.", "state", snapshot.Inbox)
	writeStateMetrics(w, "bots_tasks_total", "Current tasks by status.", "status", snapshot.Tasks)
	writeStateMetrics(w, "bots_deliveries_total", "Current deliveries by state.", "state", snapshot.Deliveries)
	fmt.Fprint(w, "# HELP bots_knowledge_active Current active knowledge records.\n# TYPE bots_knowledge_active gauge\n")
	fmt.Fprintf(w, "bots_knowledge_active %d\n", snapshot.KnowledgeActive)
}
