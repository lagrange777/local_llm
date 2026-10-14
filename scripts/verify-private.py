#!/usr/bin/env python3
"""Check the deployed private service with real HTTP requests; never write credentials."""
import argparse
import concurrent.futures
import datetime
import json
from pathlib import Path
import statistics
import threading
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,req,fp,code,msg,headers,newurl):
        raise urllib.error.HTTPError(req.full_url,code,'redirect refused',headers,fp)

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url',default='http://127.0.0.1:8081')
    p.add_argument('--token-file',type=Path,required=True)
    p.add_argument('--output',type=Path,default=Path('artifacts/private30'))
    p.add_argument('--phase',choices=['all','stability','limits','parallel','rate','cancel','timeout','smoke'],default='all')
    p.add_argument('--sequential',type=int,default=20)
    args=p.parse_args()
    u=urlsplit(args.url)
    if u.username or u.password or u.query or u.fragment or u.path not in ('','/') or (u.scheme!='https' and not (u.scheme=='http' and u.hostname in ('localhost','127.0.0.1','::1'))):
        p.error('Use HTTPS origin or loopback HTTP; credentials in URL are forbidden')
    if not 1<=args.sequential<=100:p.error('--sequential must be 1-100')
    token=args.token_file.read_text().strip();base=args.url.rstrip('/')
    args.output.mkdir(parents=True,exist_ok=True)
    report={'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'url':base,'phase':args.phase,'scope':'loopback Docker' if u.hostname in ('localhost','127.0.0.1','::1') else 'network endpoint','complete':False,'checks':[]}
    lock=threading.Lock()
    def save():
        path=args.output/(args.phase+'.json');temporary=path.with_suffix('.tmp')
        temporary.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');temporary.replace(path)
    def record(name,passed,**details):
        with lock:
            report['checks'].append({'name':name,'passed':bool(passed),**details});save()
        print(name+': '+('PASS' if passed else 'FAIL'),flush=True)
    def request(path,body=None,auth=True,headers=None,timeout=310):
        data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None
        h={'Content-Type':'application/json'}
        if auth:h['Authorization']='Bearer '+token
        h.update(headers or {})
        req=urllib.request.Request(base+path,data=data,headers=h)
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
        begin=time.monotonic()
        try:
            with opener.open(req,timeout=timeout) as r:
                raw=r.read(2<<20);status=r.status;retry=r.headers.get('Retry-After')
        except urllib.error.HTTPError as e:
            status=e.code;raw=e.read(2<<20);retry=e.headers.get('Retry-After')
        try:
            events=[json.loads(line) for line in raw.decode().splitlines() if line.strip()]
        except (ValueError,UnicodeError):events=[]
        return {'status':status,'seconds':round(time.monotonic()-begin,4),'retry_after':retry,'events':events,'text':''.join(e.get('text','') for e in events if e.get('type')=='delta'),'code':next((e.get('code') for e in events if e.get('code')),None)}
    def state():
        r=request('/api/service/status');assert r['status']==200
        return r['events'][0]
    def reset_window():
        delay=state()['window_reset_after_seconds']
        if delay:
            print('Waiting for rate window: '+str(delay)+' s',flush=True)
            # Rate limit expiry is part of the real service test, not a lowered test threshold.

            remaining=delay+1
            while remaining:
                time.sleep(min(remaining,30));remaining-=min(remaining,30)
    def payload(query='Сколько будет 2 + 2? Ответь только числом.',rag=False):
        return {'messages':[{'role':'user','content':query}],'think':False,'rag':rag}
    def done(r):
        return r['status']==200 and r['events'] and r['events'][-1].get('type')=='done'
    def idle(timeout=12):
        deadline=time.monotonic()+timeout
        while time.monotonic()<deadline:
            s=state()
            if s['active']==0 and s['queued']==0:return True
            time.sleep(.25)
        return False
    report['service']=state()
    status=request('/api/status')
    report['model']=status['events'][0]
    phases=['stability','limits','parallel','rate','cancel'] if args.phase=='all' else [args.phase]
    for phase in phases:
        if phase=='stability':
            for i in range(args.sequential):
                retries=[]
                for attempt in range(8):
                    r=request('/api/chat',payload())
                    if r['status']!=429 or r['code']!='rate_limit':break
                    retries.append(r);time.sleep(max(1,int(r['retry_after'] or 1))+1)
                record('sequential-'+str(i+1),done(r) and r['text'].strip()=='4',response=r,rate_retries=retries)
        elif phase=='limits':
            for path in ('/api/status','/api/service/status','/api/optimization/report','/api/rag/search?query=history'):
                r=request(path,auth=False);record('unauthorized-'+path,r['status']==401,response=r)
            r=request('/api/status',headers={'Authorization':'Bearer incorrect'});record('wrong-key',r['status']==401,response=r)
            for name,body,headers,expected,code in [
                ('foreign-origin',payload(),{'Origin':'https://foreign.example'},403,None),
                ('body-limit',payload('x'*65537),{},413,None),
                ('context-limit',payload('\U0001f600'*4000),{},400,'context_budget'),
                ('arbitrary-model',dict(payload(),model='arbitrary'),{},400,None),
                ('arbitrary-options',dict(payload(),options={'num_ctx':32768}),{},400,None),
            ]:
                r=request('/api/chat',body,headers=headers);record(name,r['status']==expected and (not code or r['code']==code),response=r)
            profiles=request('/api/optimization/profiles')['events'][0]['profiles']
            config=state()['limits']
            record('all-profiles-capped',all(x['options']['num_ctx']<=config['max_context_tokens'] and x['options']['num_predict']<=config['max_output_tokens'] for x in profiles),profiles=profiles)
        elif phase=='parallel':
            reset_window();s=state();count=1+s['limits']['queue_capacity']+2
            if count>s['limits']['requests_per_minute']:raise RuntimeError('Rate limit too small for isolated concurrency test')
            barrier=threading.Barrier(count);snapshots=[]
            def run():
                barrier.wait()
                return request('/api/chat',payload('Перечисли числа от 1 до 20 через запятую. Без пояснений.'))
            with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
                futures=[pool.submit(run) for _ in range(count)]
                while not all(f.done() for f in futures):
                    snapshots.append(state());time.sleep(.25)
                responses=[f.result() for f in futures]
            accepted=[r for r in responses if done(r)]
            rejected=[r for r in responses if r['status']==429 and r['code']=='queue_full' and r['retry_after']]
            record('bounded-parallel',len(accepted)==1+s['limits']['queue_capacity'] and len(rejected)==2 and all(x['active']<=1 and x['queued']<=s['limits']['queue_capacity'] for x in snapshots),responses=responses,snapshots=snapshots)
            record('parallel-slots-released',idle())
        elif phase=='rate':
            reset_window();count=state()['limits']['requests_per_minute'];responses=[]
            for i in range(count+1):
                responses.append(request('/api/chat',payload('Какова температура ядра Юпитера в базе проекта?',rag=True)))
            record('rolling-rate-limit',all(done(r) for r in responses[:count]) and responses[-1]['status']==429 and responses[-1]['code']=='rate_limit' and int(responses[-1]['retry_after'] or 0)>0,responses=responses)
        elif phase=='cancel':
            reset_window()
            req=urllib.request.Request(base+'/api/chat',data=json.dumps(payload('Опиши историю вычислительной техники подробно.')).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
            client=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
            r=client.open(req,timeout=310);first=json.loads(r.readline());r.close()
            record('cancel-releases-generation',first.get('type')=='start' and idle(),first_event=first,state=state())
        elif phase=='timeout':
            assert state()['timeout_seconds']<=2,'Use a temporary 1s timeout deployment for this phase'
            r=request('/api/chat',payload('Подробно опиши историю вычислительной техники, не менее 200 слов.'))
            record('generation-timeout',r['status']==200 and r['code']=='timeout' and idle(),response=r,state=state())
        elif phase=='smoke':
            r=request('/api/chat',payload());record('restart-generation',done(r) and r['text'].strip()=='4',response=r)
    report['final_service']=state()
    times=[x['response']['seconds'] for x in report['checks'] if x['name'].startswith('sequential-') and x['passed']]
    report['sequential_median_seconds']=statistics.median(times) if times else None
    report['complete']=True;report['passed']=all(x['passed'] for x in report['checks']);report['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();save()
    if not report['passed']:raise SystemExit('Some checks failed; raw results preserved')
if __name__=='__main__':main()
