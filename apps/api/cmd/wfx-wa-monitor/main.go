// wfx-wa-monitor is the apl.whatsapp service module's process
// (modules/apl, ADR 0028): one per WhatsApp account.
//
//	wfx-wa-monitor --label biz [--adapter adapters/apl-whatsapp.js]
//	               [--state-dir ~/.local/state/wfnexus] [--interval 30s] [--debounce 3s] [--once]
//
// Started by a wfnexus supervisor (`wfx module start apl.whatsapp`),
// which sets WFX_URL, WFX_SERVICE and WFX_SERVICE_TOKEN. Watches are read from
// wfnexus every tick (`wfx module watch add …`), and the loop heartbeats after
// every read, so a hung read shows as silent in `wfx module ls`.
// A watch: {"name","chat":"<jid>|*","hook":"https://…/hooks/p/wf","tokenEnv":"VAR","kinds":["message"]}
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/supervisor"
	"github.com/muthuishere/wfnexus/apps/api/internal/wamonitor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wfx-wa-monitor:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	label := flag.String("label", "biz", "apl whatsapp label (the account)")
	adapter := flag.String("adapter", "adapters/apl-whatsapp.js", "the channel adapter script")
	stateDir := flag.String("state-dir", filepath.Join(home, ".local", "state", "wfnexus"), "cursor, seen ids and lock live here")
	interval := flag.Duration("interval", 30*time.Second, "how often to read the account")
	debounce := flag.Duration("debounce", 3*time.Second, "hold a chat's messages until it is quiet this long")
	once := flag.Bool("once", false, "one read + flush, then exit")
	flag.Parse()
	self, err := supervisor.FromEnv()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		return err
	}
	release, err := wamonitor.Lock(filepath.Join(*stateDir, "wa-monitor-"+*label+".lock"))
	if err != nil {
		return err
	}
	defer release()
	m, err := wamonitor.New(wamonitor.Config{
		Receive: []string{"node", *adapter, "receive"},
		Env:     []string{"APL_WA_LABEL=" + *label},
		Watches: func(ctx context.Context) ([]wamonitor.Watch, error) {
			var ws []wamonitor.Watch
			return ws, self.Watches(ctx, &ws)
		},
		Heartbeat: self.Beat,
		Outbox:    self.Outbox,
		Report:    self.Report,
		Send:      []string{"node", *adapter, "send"},
		StateFile: filepath.Join(*stateDir, "wa-monitor-"+*label+".json"),
		Interval:  *interval,
		Debounce:  *debounce,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		if err := m.Read(ctx); err != nil {
			return err
		}
		m.Flush(ctx, true)
		return nil
	}
	return m.Run(ctx)
}
