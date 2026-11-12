#!/bin/sh
# Started by mediamtx (runOnAvailable) when the "live" path gets a publisher (OBS).
# Runs the multicast fanout in a restart loop; mediamtx kills us when OBS stops.
# Public outputs (Vimeo, YouTube...) are forwarded by mediamtx, controlled by DoxaBoard.
set -u

SRC="rtsp://127.0.0.1:8554/${MTX_PATH:-live}"

trap 'trap - TERM INT EXIT; kill 0' TERM INT EXIT

if [ -n "${MULTICAST_ADDR:-}" ]; then
  DEST="udp://${MULTICAST_ADDR}?ttl=${MULTICAST_TTL:-2}&pkt_size=1316"
  if [ -n "${MULTICAST_IFACE_ADDR:-}" ]; then
    DEST="${DEST}&localaddr=${MULTICAST_IFACE_ADDR}"
  fi
  (
    while true; do
      echo "multicast: sending ${SRC} -> ${MULTICAST_ADDR}"
      ffmpeg -hide_banner -loglevel warning -rtsp_transport tcp -i "$SRC" \
        -map 0 -c copy -f mpegts -mpegts_flags resend_headers "$DEST"
      sleep 2
    done
  ) &
fi


wait
