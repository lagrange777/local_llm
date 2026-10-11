#!/usr/bin/env python3
"""Copy one existing Ollama model into the Compose volume without downloading it."""
import argparse
import json
from pathlib import Path
import re
import shlex
import subprocess

project = Path(__file__).resolve().parents[1]

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--source", type=Path, default=Path("data/models"))
parser.add_argument("--model", default="qwen3:8b")
args = parser.parse_args()
running = subprocess.run(["docker", "compose", "ps", "--status", "running", "-q", "ollama"],cwd=project,capture_output=True,text=True,check=True)
if running.stdout.strip():
    raise SystemExit("Stop runtime Ollama before importing model files: docker compose stop ollama")

if not re.fullmatch(r"[a-zA-Z0-9_.-]+:[a-zA-Z0-9_.-]+", args.model):
    parser.error("Use a library model name and tag, e.g. qwen3:8b")
name, tag = args.model.split(":")
source = args.source.resolve()
manifest = Path("manifests/registry.ollama.ai/library") / name / tag
data = json.loads((source / manifest).read_text())
files = [manifest]
for layer in [data["config"], *data["layers"]]:
    digest = layer["digest"]
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise SystemExit("Invalid blob digest")
    blob = Path("blobs") / digest.replace(":", "-")
    if not (source / blob).is_file():
        raise SystemExit(f"Missing blob: {blob}")
    files.append(blob)
commands = ["mkdir -p /models/blobs " + shlex.quote(str(Path("/models") / manifest.parent))]
commands += ["cp " + shlex.quote(str(Path("/seed") / file)) + " " + shlex.quote(str(Path("/models") / file)) for file in files]
subprocess.run(
    ["docker", "compose", "run", "--rm", "--no-deps", "-v", f"{source}:/seed:ro",
     "--entrypoint", "/bin/sh", "model-init", "-ec", "\n".join(commands)],
    cwd=Path(__file__).resolve().parents[1], check=True,
)
print(f"Imported {args.model} into the Docker model volume.")
