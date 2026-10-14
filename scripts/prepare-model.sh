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
case "${RAG_PROFILE:-baseline}" in
  instruct-4b-q4) OLLAMA_MODEL=rag29-instruct-4b-q4 ;;
  instruct-4b-q8) OLLAMA_MODEL=rag29-instruct-4b-q8 ;;
esac
if ollama show "$OLLAMA_MODEL" >/dev/null 2>&1; then
  echo "Local model already present: $OLLAMA_MODEL"
else
  case "$OLLAMA_MODEL" in
    rag29-baseline-8b) source=qwen3:8b ;;
    rag29-instruct-4b-q4) source=qwen3:4b-instruct-2507-q4_K_M ;;
    rag29-instruct-4b-q8) source=qwen3:4b-instruct-2507-q8_0 ;;
    *) source=$OLLAMA_MODEL ;;
  esac
  if ! ollama show "$source" >/dev/null 2>&1; then ollama pull "$source"; fi
  if [ "$source" != "$OLLAMA_MODEL" ]; then ollama cp "$source" "$OLLAMA_MODEL"; fi
fi
