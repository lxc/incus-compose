package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// handleEvent dispatches one lifecycle event to the matching handler.
func (r *Runner) handleEvent(ctx context.Context, event incusApi.Event) {
	var lc incusApi.EventLifecycle
	if err := json.Unmarshal(event.Metadata, &lc); err != nil {
		slog.Debug("Decoding lifecycle event", "error", err)
		return
	}

	if lc.Name == "" {
		return
	}

	switch lc.Action {
	case incusApi.EventLifecycleInstanceStarted:
		slog.Debug("New lifecycle event", "instance", lc.Name, "action", lc.Action)
		r.handleStarted(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceUpdated:
		slog.Debug("New lifecycle event", "instance", lc.Name, "action", lc.Action)
		r.handleUpdated(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceDeleted:
		slog.Debug("New lifecycle event", "instance", lc.Name, "action", lc.Action)
		r.handleDeleted(ctx, lc.Name)
	case incusApi.EventLifecycleInstanceStopped, incusApi.EventLifecycleInstanceShutdown:
		slog.Debug("New lifecycle event", "instance", lc.Name, "action", lc.Action)
		r.handleStopped(ctx, lc.Name)
	}
}

// handleStarted starts a checker for a newly created or started instance.
func (r *Runner) handleStarted(ctx context.Context, name string) {
	r.mu.Lock()
	_, tracked := r.tracked[name]
	r.mu.Unlock()
	if tracked {
		return
	}

	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		slog.Debug("Fetching instance for lifecycle event", "instance", name, "error", err)
		return
	}

	if isIgnored(inst.Config) || !hasHealthCheck(inst.Config) {
		return
	}

	cfg, err := parseInstance(inst.Config, inst.StatusCode == incusApi.Running)
	if err != nil {
		slog.Warn("Parsing instance config", "instance", name, "error", err)
		return
	}

	r.spawn(ctx, name, cfg, true, false)
}

// handleUpdated replaces the checker when its params changed; self-caused writes are skipped.
func (r *Runner) handleUpdated(ctx context.Context, name string) {
	r.mu.Lock()
	if ti, ok := r.tracked[name]; ok && ti.selfWrites > 0 {
		ti.selfWrites--
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		slog.Debug("Fetching instance for update event", "instance", name, "error", err)
		return
	}

	r.mu.Lock()
	_, tracked := r.tracked[name]
	r.mu.Unlock()

	if isIgnored(inst.Config) || !hasHealthCheck(inst.Config) {
		if tracked {
			// No longer relevant: same debounced drop a real delete uses.
			r.handleDeleted(ctx, name)
		}
		return
	}

	cfg, err := parseInstance(inst.Config, inst.StatusCode == incusApi.Running)
	if err != nil {
		slog.Warn("Parsing instance config", "instance", name, "error", err)
		return
	}

	// When not running just update the tracked instance.
	if !cfg.Running {
		r.mu.Lock()
		ti, ok := r.tracked[name]
		if ok {
			ti.serverParams = cfg
		}
		r.mu.Unlock()

		return
	}

	r.mu.Lock()
	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		// Not tracked yet (healthcheck just added); start fresh, nothing to debounce.
		r.spawn(ctx, name, cfg, true, false)
		return
	}

	ti.serverParams = cfg
	r.mu.Unlock()

	r.debounce(ctx, name, ti)
}

// handleDeleted marks the instance for removal; a pending delete supersedes an update.
func (r *Runner) handleDeleted(ctx context.Context, name string) {
	r.mu.Lock()

	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}

	ti.pendingDelete = true
	r.mu.Unlock()

	r.debounce(ctx, name, ti)
}

// handleStopped evaluates restart policy, the same path a retries-exhausted checker uses.
func (r *Runner) handleStopped(ctx context.Context, name string) {
	r.mu.Lock()
	_, ok := r.tracked[name]
	r.mu.Unlock()
	if !ok {
		return
	}

	r.evaluateBackoff(ctx, name)
}

// debounce acts at once when outside the window with nothing pending, else lets the
// timer settle - it re-reads pending state when it fires, so bursts need no reschedule.
func (r *Runner) debounce(ctx context.Context, name string, ti *trackedInstance) {
	r.mu.Lock()

	now := time.Now()
	if ti.debounce == nil && now.Sub(ti.lastNotify) > debounceWindow {
		ti.lastNotify = now

		if ti.pendingDelete {
			ti.cancel()
			delete(r.tracked, name)

			r.mu.Unlock()
			return
		}

		if ti.serverParams.equal(ti.knownParams) {
			r.mu.Unlock()
			return
		}

		ti.cancel()
		cfg := ti.serverParams
		r.mu.Unlock()

		r.spawn(ctx, name, cfg, false, false)
		return
	}

	ti.debounce = time.AfterFunc(debounceWindow, func() {
		r.mu.Lock()

		cur, ok := r.tracked[name]
		if !ok {
			r.mu.Unlock()
			return
		}
		cur.debounce = nil
		cur.lastNotify = time.Now()

		if cur.pendingDelete {
			cur.cancel()
			delete(r.tracked, name)

			r.mu.Unlock()
			return
		}

		if cur.serverParams.equal(cur.knownParams) {
			r.mu.Unlock()
			return
		}

		cur.cancel()
		cfg := cur.serverParams
		r.mu.Unlock()

		r.spawn(ctx, name, cfg, false, false)
	})

	r.mu.Unlock()
}

// watch receives one checker generation's statusCh/exitCh on its own goroutine.
func (r *Runner) watch(name string, statusCh <-chan string, exitCh <-chan error) {
	for {
		select {
		case status := <-statusCh:
			r.handleCheckerStatus(name, status)
		case err := <-exitCh:
			r.handleCheckerExit(name, err)
			return
		}
	}
}

// handleCheckerStatus suppresses the resulting self-caused event and resets backoff when healthy.
func (r *Runner) handleCheckerStatus(name, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ti, ok := r.tracked[name]
	if !ok {
		return
	}

	ti.selfWrites++
	if status == shared.HealthStatusHealthy {
		ti.restartDelay = baseRestartDelay(ti.knownParams)
	}
}

// handleCheckerExit reacts only to a retries-exhausted exit; a nil exit was already handled.
func (r *Runner) handleCheckerExit(name string, err error) {
	slog.Debug("Checker exited", "instance", name, "reason", err)

	// handleStarted skips a tracked name, so an entry outliving its checker is
	// never given a new one. Drop it and let instance-started spawn afresh.
	if errors.Is(err, ErrInstanceStopped) {
		r.mu.Lock()
		ti, ok := r.tracked[name]
		if ok {
			ti.cancel()
			delete(r.tracked, name)
		}
		r.mu.Unlock()

		return
	}

	if !errors.Is(err, ErrRetriesExhausted) {
		return
	}

	r.mu.Lock()
	_, ok := r.tracked[name]
	r.mu.Unlock()
	if !ok {
		return
	}

	r.evaluateBackoff(context.Background(), name)
}

// evaluateBackoff respawns after a doubling delay or drops tracking, per restart policy.
func (r *Runner) evaluateBackoff(ctx context.Context, name string) {
	r.mu.Lock()
	ti, ok := r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	restart := ti.serverParams.Restart
	cfg := ti.serverParams
	delay := ti.restartDelay
	r.mu.Unlock()

	respawn := restart != "" && restart != "no"
	if respawn && restart == "unless-stopped" && r.isMarkedStopped(name) {
		respawn = false
	}

	r.mu.Lock()
	ti, ok = r.tracked[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	if !respawn {
		ti.cancel()
		delete(r.tracked, name)
		r.mu.Unlock()
		return
	}
	ti.cancel()
	ti.restartDelay = min(delay*2, maxRestartDelay)
	r.mu.Unlock()

	// The delay outlives the decision, so re-check it.
	time.AfterFunc(delay, func() {
		r.mu.Lock()
		_, ok := r.tracked[name]
		r.mu.Unlock()

		if !ok || (restart == "unless-stopped" && r.isMarkedStopped(name)) {
			return
		}

		r.spawn(ctx, name, cfg, true, true)
	})
}

// isMarkedStopped reports whether the instance was stopped on purpose.
func (r *Runner) isMarkedStopped(name string) bool {
	inst, _, err := r.conn.GetInstance(name)
	if err != nil {
		// Only the config key states intent; treating an error as intent turns
		// restart: unless-stopped into never restart.
		slog.Debug("Fetching instance to check the stopped marker", "instance", name, "error", err)
		return false
	}

	return inst.Config[shared.HealthStoppedKey] == "true"
}

// restartInstance starts the instance, force-stopping first unless it is already stopped.
func (r *Runner) restartInstance(name string) error {
	conn := r.conn

	state, _, err := conn.GetInstanceState(name)
	if err != nil {
		return err
	}

	if state.StatusCode != incusApi.Stopped {
		stopReq := incusApi.InstanceStatePut{
			Action:  "stop",
			Timeout: -1,
			Force:   true,
		}

		op, err := conn.UpdateInstanceState(name, stopReq, "")
		if err != nil {
			return err
		}

		if err := op.Wait(); err != nil {
			return err
		}
	}

	startReq := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err := conn.UpdateInstanceState(name, startReq, "")
	if err != nil {
		return err
	}

	return op.Wait()
}

// spawn (re)starts a checker for name, reusing any tracked entry so in-flight events find it.
func (r *Runner) spawn(ctx context.Context, name string, cfg instanceConfig, inStart, restartInstance bool) {
	if restartInstance {
		if err := r.restartInstance(name); err != nil {
			slog.Warn("restarting instance before respawn", "instance", name, "error", err)
		}
	}

	checkerCtx, cancel := context.WithCancel(ctx)
	statusCh := make(chan string, 4)
	exitCh := make(chan error, 1)

	r.mu.Lock()
	ti, existed := r.tracked[name]
	if !existed {
		ti = &trackedInstance{restartDelay: baseRestartDelay(cfg)}
		r.tracked[name] = ti
	}
	ti.cancel = cancel
	ti.knownParams = cfg
	ti.serverParams = cfg
	ti.pendingDelete = false
	ti.selfWrites = 0
	r.mu.Unlock()

	slog.Info("Starting instance checker", "instance", name, "config", cfg)

	go newChecker(r.conn, name, cfg, statusCh, exitCh).run(checkerCtx, inStart)
	go r.watch(name, statusCh, exitCh)
}

// resync reconciles discover() against tracked: start new, kill gone, leave the rest.
func (r *Runner) resync(ctx context.Context) error {
	discovered, err := discover(r.conn)

	r.mu.Lock()
	var toKill []string
	for name := range r.tracked {
		if _, ok := discovered[name]; !ok {
			toKill = append(toKill, name)
		}
	}
	var toStart []string
	for name, cfg := range discovered {
		if !cfg.Running {
			continue
		}

		_, ok := r.tracked[name]
		if !ok {
			toStart = append(toStart, name)
		}
	}
	for _, name := range toKill {
		ti := r.tracked[name]
		delete(r.tracked, name)
		ti.cancel()
	}
	r.mu.Unlock()

	for _, name := range toStart {
		r.spawn(ctx, name, discovered[name], true, false)
	}

	return err
}
