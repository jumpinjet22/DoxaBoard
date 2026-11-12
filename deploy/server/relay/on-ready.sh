#!/bin/sh
# Started by mediamtx when the "live" path gets a publisher (OBS).
# Runs each output in its own restart loop; mediamtx kills us when OBS stops.
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

if [ -n "${VIMEO_RTMP_URL:-}" ]; then
  (
    while true; do
      echo "vimeo: pushing ${SRC}"
      ffmpeg -hide_banner -loglevel warning -rtsp_transport tcp -i "$SRC" \
        -map 0 -c copy -f flv "$VIMEO_RTMP_URL"
      sleep 5
    done
  ) &
fi

wait
