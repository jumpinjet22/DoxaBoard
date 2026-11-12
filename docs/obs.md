# OBS setup

OBS encodes once and sends to the DoxaCast relay. The relay fans out to
screens (multicast + unicast) and Vimeo. You don't need NDI.

## Stream

Settings → Stream:

- **Service:** Custom
- **Server:** `srt://<server-ip>:8890?streamid=publish:live&pkt_size=1316`
- **Stream key:** leave empty

RTMP also works (`rtmp://<server-ip>/live`), but SRT handles packet loss better.

If OBS's main Stream output is already used for something else, send to the
relay from a second output (for example with a multistream plugin), or point
Recording → Custom Output (FFmpeg) at the SRT URL.

## Output

Settings → Output → Advanced → Streaming:

| Setting | Value | Why |
|---|---|---|
| Encoder | Hardware H.264 (NVENC / QuickSync / AMF) if available | Vimeo needs H.264; Pi 4 decodes it in hardware |
| Rate control | CBR | Predictable bandwidth |
| Bitrate | 6000 Kbps | Plenty for 1080p30 on a TV |
| Keyframe interval | 2 s | Vimeo requirement; faster joins for screens |
| Profile | high | |
| B-frames | 0–2 | 0 = lowest latency |

Settings → Video: **1920×1080, 30 fps**.

Settings → Audio: **48 kHz**, AAC **160 Kbps**.

## Check it

- Console → Screens: screens should switch to *Live* within ~10 seconds of going live.
- On any machine: `ffplay "srt://<server-ip>:8890?streamid=read:live"`
- If Vimeo is configured, its event page should show the stream as live.
