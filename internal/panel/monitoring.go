package panel

import (
	"context"
	"sync"
	"time"
)

// Register before launching so shutdown cannot close SQLite under collectors.
func (a *App) StartCollection(parent context.Context) {
	m := a.operations
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		defer cancel()
		if m.ctx.Err() != nil {
			return
		}
		a.collectLoop(ctx)
	}()
}
func (a *App) collectLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var lastPrune time.Time
	for {
		servers, err := a.servers()
		if err == nil {
			var wg sync.WaitGroup
			limit := make(chan struct{}, 4)
			for _, s := range servers {
				select {
				case limit <- struct{}{}:
				case <-ctx.Done():
					wg.Wait()
					return
				}
				wg.Add(1)
				go func(s Server) {
					defer wg.Done()
					defer func() { <-limit }()
					state := a.collect(ctx, s)
					a.mu.RLock()
					last := a.docker[s.ID]
					a.mu.RUnlock()
					if state.Status != "online" {
						a.dockerFailed(s, state.Error)
					} else if time.Since(last.attempt) >= 29*time.Second {
						a.collectDocker(ctx, s, state.Sample.Cores)
					}
				}(s)
			}
			wg.Wait()
			if time.Since(lastPrune) >= time.Hour {
				if _, err := a.db.Exec("DELETE FROM samples WHERE at < ?", time.Now().UTC().Add(-7*24*time.Hour).Format(time.RFC3339Nano)); err == nil {
					lastPrune = time.Now()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) collect(ctx context.Context, s Server) State {
	out, err := a.run(ctx, s, metricsCommand)
	state := State{Status: "offline"}
	if err != nil {
		state.Error = err.Error()
	} else {
		sample, e := parseMetrics(out)
		if e != nil {
			state.Error = e.Error()
		} else {
			state = State{Status: "online", Sample: &sample}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sameConnection(s) {
		return State{Status: "pending"}
	}
	if sample := state.Sample; sample != nil {
		if _, err := a.db.Exec("INSERT INTO samples(server_id,at,cpu,memory,disk,uptime,rx,tx) VALUES(?,?,?,?,?,?,?,?)", s.ID, sample.At, sample.CPU, sample.Memory, sample.Disk, sample.Uptime, sample.RX, sample.TX); err != nil {
			state.Error = "Conexión activa; no se pudo guardar el histórico"
		}
	}
	if state.Sample == nil {
		state.Sample = a.states[s.ID].Sample
	}
	a.states[s.ID] = state
	return state
}
