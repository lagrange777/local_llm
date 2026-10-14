#!/usr/bin/env python3
"""Produce a concise report from private service integration results."""
import json
from pathlib import Path
root=Path('artifacts/private30')
reports=[(p.stem,json.loads(p.read_text())) for p in root.glob('*.json') if p.stem in ('all','timeout','smoke')]
lines=['# Private LLM service — task 30','','Executed on loopback Docker Desktop / ARM64 CPU with the real local Qwen model.',
'The user explicitly requested the Docker package and local checks for now. VPS deployment, Tailscale enrollment/HTTPS, and access from a second device are deferred.',
'The loopback run validates the application and Docker limits; it is not evidence of a deployed remote private service.','','| Run | Complete | Passed | Checks |','|---|---|---|---|']
for name,r in reports:
 lines.append(f"| {name} | {r['complete']} | {r.get('passed',False)} | {sum(x['passed'] for x in r['checks'])}/{len(r['checks'])} |")
 for c in r['checks']:
  lines.append('')
  lines.append('**'+name+' / '+c['name']+'**: '+('PASS' if c['passed'] else 'FAIL'))
  if 'response' in c:lines.append(f"HTTP {c['response']['status']}, {c['response']['seconds']:.3f} s. Answer: {c['response']['text'] or c['response'].get('code') or 'none'}")
  if c['name']=='bounded-parallel':
   lines.append('HTTP statuses: '+str([x['status'] for x in c['responses']]))
   lines.append('Max active/queued: '+str(max(x['active'] for x in c['snapshots']))+'/'+str(max(x['queued'] for x in c['snapshots'])))
  if c['name']=='rolling-rate-limit':lines.append('HTTP statuses: '+str([x['status'] for x in c['responses']]))
 if r.get('sequential_median_seconds') is not None:lines+=['',f"Sequential successful-response median: {r['sequential_median_seconds']:.3f} s."]
lines+=['','## Artifacts','','- [Sequential, parallel, auth and limit checks](all.json)',
'- [Real generation timeout](timeout.json)','- [Generation after application restart](smoke.json)',
'- [Docker resources, isolation and sanitized logs](environment.json)',
'- [Login UI](login.jpg)','- [Private chat UI](chat.jpg)',
'','Credentials and session cookies are excluded. Tests use explicit public math questions; production logs contain only fixed route labels, status, timing and token count.',
'The HTTP test override disables Secure cookies only for loopback. The production configuration requires HTTPS and Secure cookies.']
(root/'REPORT.md').write_text('\n'.join(lines)+'\n')
