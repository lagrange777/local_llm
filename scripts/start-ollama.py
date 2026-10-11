#!/usr/bin/env python3
"""Start a detached, local-only Ollama daemon for this project."""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
binary = shutil.which("ollama")
if not binary:
    raise SystemExit("Install Ollama first: brew install ollama")
url = "http://127.0.0.1:11434/api/version"
try:
    with urllib.request.urlopen(url, timeout=2) as response:
        version = json.load(response)
    raise SystemExit(f"Ollama already responds on port 11434 ({version}); stop it before starting this project's daemon.")
except (urllib.error.URLError, TimeoutError, socket.timeout):
    pass
runtime = root / "tmp"
models = root / "data" / "models"
runtime.mkdir(exist_ok=True)
models.mkdir(parents=True, exist_ok=True)
environment = os.environ.copy()
environment.update({
    "OLLAMA_HOST": "127.0.0.1:11434",
    "OLLAMA_NO_CLOUD": "1",
    "OLLAMA_MODELS": str(models),
    "OLLAMA_CONTEXT_LENGTH": "4096",
})
with (runtime / "ollama.log").open("ab") as log:
    process = subprocess.Popen([binary, "serve"], env=environment, stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
(runtime / "ollama.pid").write_text(str(process.pid) + "\n")
deadline = time.monotonic() + 30
while time.monotonic() < deadline:
    if process.poll() is not None:
        raise SystemExit(f"Ollama exited: see {runtime / 'ollama.log'}")
    try:
        with urllib.request.urlopen(url, timeout=1) as response:
            version = json.load(response)
        print(f"Ollama {version['version']} running locally: http://127.0.0.1:11434 (PID {process.pid})")
        break
    except (urllib.error.URLError, TimeoutError, socket.timeout):
        time.sleep(0.5)
else:
    process.terminate()
    raise SystemExit("Ollama did not become ready within 30 seconds.")
