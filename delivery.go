package main

import (
	"fmt"
	"time"
)

func (p *proxyState) dropLate(d drop) bool {
	return p.up.autoInsertWindow <= 0 || !time.Now().Before(d.accepted.Add(p.up.autoInsertWindow))
}

func (p *proxyState) insertDrop(d drop, payload string) bool {
	if p.up.autoInsertWindow <= 0 {
		return false
	}
	return p.injectBefore(payload, d.bracketed, d.accepted.Add(p.up.autoInsertWindow))
}

func (p *proxyState) keepLate(d drop, paths []string, failure error) {
	r := PendingResult{Target: p.up.target, Source: d.source, Files: baseNames(d.tokens), Paths: paths, OK: failure == nil}
	if failure != nil {
		r.Error = failure.Error()
	}
	saved, err := savePending(p.stateDir, r)
	if err != nil {
		Notify("moshdrop", fmt.Sprintf(msg("pending.savefailed"), err.Error()))
		return
	}
	if !saved.OK {
		Notify("moshdrop", fmt.Sprintf(msg("pending.latefailed"), saved.ID, saved.Error))
		return
	}
	if err := notifyPending(p.stateDir, saved); err != nil {
		Notify("moshdrop", fmt.Sprintf(msg("pending.fallback"), saved.ID))
	}
}
