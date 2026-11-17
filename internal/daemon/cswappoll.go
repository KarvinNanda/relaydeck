package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/KarvinNanda/relaydeck/internal/cswap"
)

// Account is what relaydeck knows about cswap's default login.
type Account struct {
	Cswap   bool   `json:"cswap"`   // cswap is installed and used (never on windows)
	Known   bool   `json:"known"`   // the last cswap status call worked
	Default string `json:"default"` // default account, only when Known
}

// pollCswap reads `cswap status` now and every CswapEvery (ruling 13).
func (d *Daemon) pollCswap(ctx context.Context) {
	if d.opts.CswapPath == "" {
		return
	}
	t := time.NewTicker(d.opts.CswapEvery)
	defer t.Stop()
	for {
		d.refreshAccount(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshAccount never guesses: an error means "account unknown".
func (d *Daemon) refreshAccount(ctx context.Context) {
	next := Account{Cswap: true}
	info, err := d.opts.CswapStatus(ctx, d.opts.CswapPath)
	if err == nil {
		next.Known, next.Default = true, info.DefaultAccount
	} else if ctx.Err() == nil {
		// The error text can carry cswap's stderr (account names), so only
		// the kind is logged.
		kind := "command failed"
		if errors.Is(err, cswap.ErrUnknownFormat) {
			kind = "unknown output format"
		}
		d.log.Warn("cswap status failed, account unknown", "error", kind)
	}
	d.acctMu.Lock()
	changed := next != d.acct
	d.acct = next
	d.acctMu.Unlock()
	if changed {
		a := next
		d.hub.publish(Event{Type: "account", Account: &a})
	}
}

func (d *Daemon) account() Account {
	d.acctMu.Lock()
	defer d.acctMu.Unlock()
	return d.acct
}
