# Network

## Bandwidth

A 1080p30 stream at ~6 Mbps is the budget for everything below.

- **Multicast:** the server sends one copy, whatever the number of screens.
- **Unicast** (SRT/RTSP/HLS): each screen pulls its own copy. 1 GbE on the
  server handles ~100 screens comfortably.
- **Vimeo fallback:** each screen pulls from the internet. If *every* screen
  falls back at once, that's N × 6 Mbps of downstream. Keep a unicast source
  above Vimeo so this only happens when the relay is truly down.

## Multicast requirements

Multicast is the primary source, and it needs the network to cooperate:

1. **Wired screens.** Wi-Fi sends multicast at the lowest rate with no
   retransmits, which means blocky video. DoxaCast falls back to unicast when
   multicast doesn't arrive, but don't plan on multicast over Wi-Fi.
2. **Managed switches with IGMP snooping enabled.** Without it, many switches
   flood multicast to *every* port.
3. **An IGMP querier** on the VLAN (usually a setting on the core switch or router).
4. **Same VLAN/subnet** for the server and screens, unless you set up multicast routing (PIM).
5. **TTL 1–2** (the default) keeps the stream from leaving your LAN.

Ask whoever runs the network: *"Is IGMP snooping on, and is there an IGMP
querier, on the VLAN the screens are on?"*

**Recommended:** give the screens their own switch and VLAN. Then you control
IGMP and nothing else on the network ever sees the video.

If any of this isn't possible, nothing breaks: remove the multicast source
from the profile (or leave it; it'll just never come up) and screens use unicast.

## Moving from HDMI/HDBaseT extenders

Cable that carries HDBaseT is almost always good for gigabit Ethernet.

1. **Test every run** with a cable tester, or plug in a Pi and check
   `ethtool eth0` for the negotiated speed. Re-terminate anything odd:
   extenders sometimes use non-standard wiring.
2. **Bring the runs home** to a managed switch where the extender transmitters used to be.
3. **Migrate one screen at a time.** Old and new coexist fine.

## Long runs

Ethernet is specified to 100 m. The stream only needs ~10 Mbps, which gives you options:

| Run | Do this |
|---|---|
| < 100 m | Normal: Pi behind the TV |
| 100–150 m | Try forcing the switch port to **100 Mbps full duplex**. It's far more tolerant and has ~12× the headroom you need. Watch `rx_errors` in the console for a few days. |
| Too long for Ethernet, HDBaseT reaches | **Pi in the rack** → HDMI → existing HDBaseT transmitter |
| Too long for HDMI/HDBaseT, currently composite | **Pi 4 in the rack** → composite → existing run (see hardware.md) |
| Want HD on a very long run | Ethernet-over-coax (coax runs) or long-reach VDSL2 extenders (twisted pair). Check the product's distance vs speed specs. |

The agent reports link speed and error counters in every heartbeat, so you can
tell from the console which runs are marginal.

## Ports

| Port | Proto | What |
|---|---|---|
| 8080 | TCP | doxacast-server (API, console, photos) |
| 8890 | UDP | SRT in from OBS / out to screens |
| 1935 | TCP | RTMP in from OBS (alternative) |
| 8554 | TCP | RTSP out |
| 8888 | TCP | LL-HLS out (smart TVs, Android app) |
| 5000 | UDP | multicast group 239.10.10.1 (configurable) |
