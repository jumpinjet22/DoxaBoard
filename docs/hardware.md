# Hardware

## Raspberry Pi

| | Pi 4 | Pi 5 |
|---|---|---|
| H.264 hardware decode | ✅ | ❌ (software decode; should handle 1080p30, test first) |
| HEVC hardware decode | ✅ | ✅ |
| Composite out | ✅ 3.5 mm TRRS jack | solder pads only |
| Power supply | 5V/3A | 5V/5A (official 27W) |

Vimeo requires **H.264**, and we send one stream everywhere, so H.264 it is.
Pi 4 is the safe choice and the only sensible one for composite. Test a Pi 5
on your actual stream before buying a stack of them.

Use Raspberry Pi OS **Lite**. The agent drives the display directly through DRM (no desktop).

## Power

No PoE required. Use the official power supply on an outlet behind the TV
or in the rack.

**Do not power the Pi from the TV's USB port.** It usually can't supply enough
current, and many TVs cut USB power when they turn off. That yanks power from
the Pi every night and eventually corrupts the SD card.

Reliability without PoE's remote power-cycle:

- **Hardware watchdog:** `install.sh` enables systemd's watchdog, so a hung Pi reboots itself.
- **Atomic writes:** the agent never leaves half-written files.
- **Optional:** a USB SSD instead of an SD card, or a read-only root (planned).

## Composite output (Pi 4)

For runs too long for HDMI that carry composite today: Pi 4 in the rack →
3.5 mm A/V cable (TRRS) → existing composite run.

In `/boot/firmware/config.txt`:

```ini
# Composite out. Disables HDMI on this Pi.
enable_tvout=1
# 0 = NTSC, 2 = PAL
sdtv_mode=0
# 4:3
sdtv_aspect=1
```

On Bookworm with the KMS driver, also add `,composite` to the
`dtoverlay=vc4-kms-v3d` line and add
`video=Composite-1:720x480@60ie` (NTSC) or `video=Composite-1:720x576@50ie`
(PAL) to `/boot/firmware/cmdline.txt`. Reboot.

Then set the screen's **Output** to *composite* in the console and give it a
**safe area** (5–10%) so photo edges aren't lost to overscan.

> Verify these settings on your Pi OS version. Composite configuration has
> changed between releases. M3 on the roadmap automates this.

## Android TV / Google TV (planned, M4)

Prefer devices with **built-in Ethernet**: Google TV Streamer, onn 4K Pro.
Sticks with USB Ethernet adapters work but add a part that can wiggle loose.
Avoid Fire TV devices running Vega OS (no Android app sideloading).

## Switch

A managed switch with **IGMP snooping** and an **IGMP querier**. PoE isn't needed.
See [network.md](network.md).
