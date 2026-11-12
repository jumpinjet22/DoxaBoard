package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/jumpinjet22/doxacast/core"
)

// Prober checks sources without playing them.
type Prober struct {
	Interface   string
	FFprobePath string
	HTTP        *http.Client
}

// Probe implements core.Prober.
func (p *Prober) Probe(ctx context.Context, src core.Source) error {
	switch src.Kind {
	case core.KindMulticast:
		return p.probeMulticast(ctx, src.URL)
	case core.KindHLS:
		return p.probeHLS(ctx, src.URL)
	case core.KindSRT, core.KindRTSP:
		return p.probeFFprobe(ctx, src.URL)
	default:
		return fmt.Errorf("unsupported source kind %q", src.Kind)
	}
}

// multicastAddr extracts "239.x.x.x:port" from udp://[@]239.x.x.x:port[?...].
func multicastAddr(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "udp" {
		return "", false
	}
	host := strings.TrimPrefix(u.Host, "@")
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return "", false
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsMulticast() {
		return "", false
	}
	return host, true
}

// probeMulticast joins the group and waits for one packet. Joining is what
// makes IGMP-snooping switches forward the stream to this port at all.
func (p *Prober) probeMulticast(ctx context.Context, raw string) error {
	addr, ok := multicastAddr(raw)
	if !ok {
		return fmt.Errorf("not a multicast udp URL: %s", raw)
	}
	gaddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	var ifi *net.Interface
	if p.Interface != "" {
		ifi, _ = net.InterfaceByName(p.Interface) // nil = system default
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, gaddr)
	if err != nil {
		return fmt.Errorf("join %s: %w", addr, err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return errors.New("no multicast packets")
		}
		return err
	}
	// MPEG-TS packets start with sync byte 0x47.
	if n < 188 || buf[0] != 0x47 {
		return errors.New("multicast data is not MPEG-TS")
	}
	return nil
}

func (p *Prober) probeHLS(ctx context.Context, raw string) error {
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("playlist: %s", resp.Status)
	}
	line, err := bufio.NewReader(io.LimitReader(resp.Body, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(line, "\uFEFF")), "#EXTM3U") {
		return errors.New("not an HLS playlist")
	}
	return nil
}

// probeFFprobe asks ffprobe whether the stream has a video track.
func (p *Prober) probeFFprobe(ctx context.Context, raw string) error {
	args := []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0"}
	if strings.HasPrefix(raw, "rtsp") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args, "-i", raw)
	out, err := exec.CommandContext(ctx, p.FFprobePath, args...).Output()
	if ctx.Err() != nil {
		return errors.New("probe timed out")
	}
	if err != nil {
		return fmt.Errorf("ffprobe: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return errors.New("no video stream")
	}
	return nil
}
