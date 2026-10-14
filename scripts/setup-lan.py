#!/usr/bin/env python3
"""Prepare explicit RFC1918 HTTP LAN access; preserve the existing API key."""
import argparse
import ipaddress
import os
from pathlib import Path
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--ip',required=True,help='Current home-network IPv4 address of this server')
p.add_argument('--port',type=int,default=8081)
args=p.parse_args()
try:ip=ipaddress.IPv4Address(args.ip)
except ValueError:p.error('A literal IPv4 address is required')
private=any(ip in ipaddress.IPv4Network(s) for s in ('10.0.0.0/8','172.16.0.0/12','192.168.0.0/16'))
if not private:p.error('Use an RFC1918 home-network address, not a public or wildcard address')
if not 1024<=args.port<=65535:p.error('Port must be 1024-65535')
root=Path(__file__).resolve().parent.parent
if not (root/'.private.env').is_file() or not (root/'.secrets/service_token').is_file():
 p.error('Prepare the existing private service with scripts/setup-private.py first')
origin='http://'+str(ip)+':'+str(args.port)
env=root/'.lan.env'
env.write_text('LAN_BIND_IP='+str(ip)+'\nLAN_PORT='+str(args.port)+'\nPRIVATE_PUBLIC_ORIGIN='+origin+'\nPRIVATE_COOKIE_SECURE=false\n')
os.chmod(env,0o600)
print('LAN HTTP prepared: '+origin)
print('No encryption: use a trusted home network. Existing service key was preserved.')
