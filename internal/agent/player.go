package agent

import (
	"context"
	"time"

	"github.com/jumpinjet22/doxaboard/core"
)

// Player plays live sources with mpv.
type Player struct {
	MpvPath string
	MpvArgs []string
	Logf    func(string, ...any)

	g guarded
}

// liveArgs returns mpv arguments tuned per source kind.
func liveArgs(src core.Source) []string {
	switch src.Kind {
	case core.KindMulticast, core.KindSRT, core.KindRTSP:
		// LAN sources: keep latency low; the network is reliable enough.
		return []string{"--profile=low-latency", "--untimed=no", "--network-timeout=5"}
	default:
		// HLS (relay or Vimeo): let it buffer; it's the fallback anyway.
		return []string{"--network-timeout=10", "--cache=yes"}
	}
}

// Play stops anything playing and starts src.
func (p *Player) Play(ctx context.Context, src core.Source) error {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	p.g.proc.stop()

	args := append(baseMpvArgs(), liveArgs(src)...)
	args = append(args, "--keep-open=no", "--idle=no")
	args = append(args, p.MpvArgs...)
	args = append(args, "--", mpvURL(src))

	proc, err := startMpv(p.MpvPath, args, p.Logf)
	if err != nil {
		return err
	}
	p.g.proc = proc
	return waitBriefly(ctx, proc, 1500*time.Millisecond)
}

// mpvURL adapts URLs to what mpv/ffmpeg expect. For multicast, the "@"
// form tells ffmpeg to bind to the group address and join it.
func mpvURL(src core.Source) string {
	if src.Kind == core.KindMulticast {
		if g, ok := multicastAddr(src.URL); ok {
			return "udp://@" + g
		}
	}
	return src.URL
}

// Stop stops playback.
func (p *Player) Stop() error {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	p.g.proc.stop()
	p.g.proc = nil
	return nil
}

// Healthy reports whether mpv is still running.
func (p *Player) Healthy() bool {
	p.g.mu.Lock()
	defer p.g.mu.Unlock()
	return p.g.proc.alive()
}
