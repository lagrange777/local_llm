#!/bin/sh
set -eu
ollama serve &
server_pid=$!
trap 'kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true' EXIT INT TERM
attempt=0
until ollama list >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 45 ] && kill -0 "$server_pid" 2>/dev/null || exit 1
  sleep 1
done
# Freeze the existing checkpoint; never replace it with a fresh mutable 8B tag.
if ! ollama show rag29-baseline-8b >/dev/null 2>&1; then
  if ! ollama show qwen3:8b >/dev/null 2>&1; then ollama pull qwen3:8b; fi
  ollama cp qwen3:8b rag29-baseline-8b
fi
for quant in q4_K_M q8_0; do
  case "$quant" in q4_K_M) alias=rag29-instruct-4b-q4 ;; q8_0) alias=rag29-instruct-4b-q8 ;; esac
  if ! ollama show "$alias" >/dev/null 2>&1; then
    ollama pull "qwen3:4b-instruct-2507-$quant"
    ollama cp "qwen3:4b-instruct-2507-$quant" "$alias"
  fi
done
ollama list
