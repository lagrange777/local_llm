#!/bin/sh
set -eu
case "$OLLAMA_MODEL" in
  *cloud*|*' '*|*/*) echo "Select a local model name" >&2; exit 1 ;;
esac
ollama serve &
server_pid=$!
trap 'kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true' EXIT INT TERM
attempt=0
until ollama list >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ] || ! kill -0 "$server_pid" 2>/dev/null; then
    echo "Model preparation server did not start" >&2
    exit 1
  fi
  sleep 1
done
if ollama show "$OLLAMA_MODEL" >/dev/null 2>&1; then
  echo "Local model already present: $OLLAMA_MODEL"
else
  ollama pull "$OLLAMA_MODEL"
fi
