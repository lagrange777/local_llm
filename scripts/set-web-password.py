#!/usr/bin/env python3
"""Set a separate web password. Docker receives only the salted password hash."""
import argparse
import base64
import getpass
import hashlib
import os
from pathlib import Path
import secrets
def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--generate',action='store_true',help='Generate an initial password into a private local file')
    p.add_argument('--if-missing',action='store_true',help='Preserve an existing password hash')
    args=p.parse_args()
    root=Path(__file__).resolve().parent.parent
    directory=root/'.secrets';directory.mkdir(mode=0o700,exist_ok=True);os.chmod(directory,0o700)
    target=directory/'web_password_hash';initial=directory/'web_password.initial'
    if args.if_missing and target.exists():
        print('Existing web password hash preserved.');return
    if args.generate:password=secrets.token_urlsafe(12)
    else:
        password=getpass.getpass('New web password: ')
        if password!=getpass.getpass('Repeat password: '):p.error('Passwords do not match')
    data=password.encode('utf-8')
    if not 12<=len(data)<=256:p.error('Password must contain 12-256 UTF-8 bytes')
    api=directory/'service_token'
    if api.exists() and password==api.read_text().strip():p.error('Web password must differ from the API key')
    salt=secrets.token_bytes(16);digest=hashlib.pbkdf2_hmac('sha256',data,salt,600000,dklen=32)
    b64=lambda x:base64.b64encode(x).decode().rstrip('=')
    value='pbkdf2-sha256$600000$'+b64(salt)+'$'+b64(digest)+'\n'
    temporary=directory/'web_password_hash.tmp'
    fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
    with os.fdopen(fd,'w') as f:f.write(value)
    os.chmod(temporary,0o644);temporary.replace(target)
    if args.generate:
        fd=os.open(initial,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
        with os.fdopen(fd,'w') as f:f.write(password+'\n')
        os.chmod(initial,0o600)
        print('Initial password saved in .secrets/web_password.initial (not mounted into Docker).')
    elif initial.exists():initial.unlink()
    print('Password hash updated; API key unchanged. Recreate app to apply it and revoke sessions.')
if __name__=='__main__':main()
