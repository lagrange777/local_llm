#!/usr/bin/env python3
"""Create a private service secret and deployment environment without printing keys."""
import argparse
import os
from pathlib import Path
import secrets
import subprocess
import sys
from urllib.parse import urlsplit
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--origin',required=True,help='HTTPS Tailscale origin; loopback HTTP allowed for local testing')
args=p.parse_args();u=urlsplit(args.origin)
local=u.hostname in ('127.0.0.1','localhost','::1')
if not u.netloc or u.username or u.password or u.path or u.query or u.fragment or (u.scheme!='https' and not (u.scheme=='http' and local)):
 p.error('Use an HTTPS origin without path, or loopback HTTP for tests')
root=Path(__file__).resolve().parent.parent
secret_dir=root/'.secrets';secret_dir.mkdir(mode=0o700,exist_ok=True);os.chmod(secret_dir,0o700)
key=secret_dir/'service_token'
if not key.exists():
 with key.open('x') as f:f.write(secrets.token_hex(32)+'\n')
# Compose file-backed secrets retain the file mode: the non-root container must read it.
# Host access is restricted by the parent directory (0700).
os.chmod(key,0o644)
subprocess.run([sys.executable,str(root/'scripts/set-web-password.py'),'--generate','--if-missing'],check=True)
env=root/'.private.env'
if env.exists():
 p.error('.private.env already exists; edit it explicitly to change deployment origin. Existing key was preserved.')
env.write_text('PRIVATE_PUBLIC_ORIGIN='+args.origin+'\nPRIVATE_COOKIE_SECURE='+str(u.scheme=='https').lower()+'\nPRIVATE_PROFILE=instruct-4b-q4\n')
os.chmod(env,0o600)
print('Private service key prepared; contents were not printed.')
print('Created .private.env. Mode: '+('HTTPS' if u.scheme=='https' else 'loopback HTTP test only'))
