#!/usr/bin/env python3
"""Paired local RAG optimization experiment; fixed retrieval, resource sampling, no cloud."""
import argparse,datetime,hashlib,json,math,pathlib,platform,re,statistics,subprocess,threading,time,urllib.request,urllib.parse

def digest(value):
    return hashlib.sha256(json.dumps(value,ensure_ascii=False,sort_keys=True).encode()).hexdigest()
def atomic(path,value):
    path=pathlib.Path(path);tmp=path.with_suffix('.tmp');tmp.write_text(json.dumps(value,ensure_ascii=False,indent=2)+'\n');tmp.replace(path)
def get(base,path):
    with urllib.request.urlopen(base+path,timeout=15) as response:return json.load(response)
def command(args,timeout=20):
    result=subprocess.run(args,text=True,capture_output=True,timeout=timeout,check=True)
    return result.stdout
def vm_pid():
    rows=command(['ps','-axo','pid,comm']).splitlines()
    for row in rows:
        if 'com.apple.Virtualization.VirtualMachine' in row:
            pid=row.strip().split()[0]
            try:
                names=command(['lsof','-p',pid,'-Fn'])
                if '/com.docker.docker/' in names:return pid
            except (OSError,subprocess.SubprocessError):pass
    return None
def footprint(pid):
    if not pid:return None
    try:
        text=command(['vmmap','-summary',pid])
        match=re.search(r'Physical footprint:\s+([\d.]+)([KMG])',text)
        return float(match[1])*{'K':1024,'M':1024**2,'G':1024**3}[match[2]] if match else None
    except (OSError,subprocess.SubprocessError):return None
def sample(pid):
    text=command(['docker','compose','exec','-T','ollama','sh','-c','cat /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.stat /proc/meminfo /sys/fs/cgroup/memory.events'])
    lines=text.splitlines();current=int(lines[0]);values={}
    for line in lines[1:]:
        match=re.match(r'([^ :]+):?\s+(\d+)',line)
        if match:values[match[1]]=int(match[2])
    vm=command(['vm_stat']) if pid else ''
    page_match=re.search(r'page size of (\d+)',vm)
    compressed=re.search(r'Pages occupied by compressor:\s+(\d+)',vm)
    swap=command(['sysctl','-n','vm.swapusage']) if pid else ''
    swap_match=re.search(r'used = ([\d.]+)M',swap)
    resident=int(command(['ps','-p',pid,'-o','rss=']).strip())*1024 if pid else None
    return {'time':time.time(),'container_current_bytes':current,
            'container_working_set_bytes':max(0,current-values.get('inactive_file',0)),
            'container_anon_bytes':values.get('anon'),
            'vm_guest_used_bytes':(values['MemTotal']-values['MemAvailable'])*1024,
            'vm_guest_total_bytes':values['MemTotal']*1024,'oom_kill':values.get('oom_kill',0),
            'host_vm_rss_bytes':resident,
            'host_compressed_bytes':int(compressed[1])*int(page_match[1]) if compressed and page_match else None,
            'host_swap_used_bytes':float(swap_match[1])*1024**2 if swap_match else None}
def call(base,question,profile,repeat,pid,expected):
    body={'messages':[{'role':'user','content':question['query']}],'rag':True,'think':False,'profile':profile['id']}
    run={'profile':profile['id'],'repeat':repeat,'request':body,'complete':False,'text':'','citations':[],'events':[],'samples':[],'sample_errors':[],'error':None}
    stop=threading.Event()
    def collect():
        while not stop.is_set():
            try:run['samples'].append(sample(pid))
            except (OSError,ValueError,subprocess.SubprocessError) as error:run['sample_errors'].append(str(error))
            stop.wait(2)
    watcher=threading.Thread(target=collect,daemon=True);watcher.start()
    started=time.perf_counter()
    try:
        request=urllib.request.Request(base+'/api/chat',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
        with urllib.request.urlopen(request,timeout=360) as response:
            for line in response:
                e=json.loads(line);run['events'].append(e)
                if e['type']=='delta':run['text']+=e.get('text','')
                elif e['type']=='retrieval':
                    run['retrieval']=e['retrieval']
                    actual=[{'id':s['chunk_id'],'text':s['text']} for s in e['retrieval']['sources']]
                    if digest(actual)!=expected:raise ValueError('Retrieval changed between profiles')
                elif e['type']=='sources':
                    run['citations']=e['answer']['citations'];run['claims']=e['answer']['claims'];run['status']=e['answer']['status']
                elif e['type']=='done':run['metrics']=e;run['complete']=True
                elif e['type']=='error':run['error']=e.get('text');run['error_code']=e.get('code')
        if not run['complete'] and not run['error']:run['error']='Stream ended without done'
    except (OSError,ValueError) as error:run['error']=str(error);run['complete']=False
    finally:
        run['seconds']=time.perf_counter()-started
        stop.set();watcher.join(timeout=25)
    run['memory']={key:max((s[key] for s in run['samples'] if s.get(key) is not None),default=None) for key in ['container_current_bytes','container_working_set_bytes','vm_guest_used_bytes','host_vm_rss_bytes','host_compressed_bytes','host_swap_used_bytes']}
    run['oom_delta']=(run['samples'][-1]['oom_kill']-run['samples'][0]['oom_kill']) if run['samples'] else None
    return run
def summarize(report):
    result={}
    for p in report['profiles']:
        runs=[r for q in report['questions'] for r in q['runs'] if r['profile']==p['id']]
        good=[r for r in runs if r['complete']]
        model=[r for r in good if r.get('metrics',{}).get('attempts',0)>0]
        warm=[r for r in model if r.get('phase')!='cold']
        times=sorted(r['seconds'] for r in warm)
        rates=[r['metrics'].get('tokens',0)/r['metrics']['eval_seconds'] for r in warm if r['metrics'].get('eval_seconds',0)>0]
        peak=max((r['memory']['container_working_set_bytes'] for r in runs if r['memory']['container_working_set_bytes'] is not None),default=None)
        raw_peak=max((r['memory']['container_current_bytes'] for r in runs if r['memory']['container_current_bytes'] is not None),default=None)
        guest_peak=max((r['memory']['vm_guest_used_bytes'] for r in runs if r['memory']['vm_guest_used_bytes'] is not None),default=None)
        result[p['id']]={'requests':len(runs),'completed':len(good),'errors':len(runs)-len(good),'model_median_seconds':statistics.median(times) if times else None,'model_p95_seconds':times[math.ceil(len(times)*.95)-1] if times else None,'median_tokens_per_second':statistics.median(rates) if rates else None,'server_abstentions':sum(r.get('metrics',{}).get('attempts')==0 for r in good),'peak_container_gib':peak/1024**3 if peak is not None else None,'peak_cgroup_current_gib':raw_peak/1024**3 if raw_peak is not None else None,'peak_vm_guest_used_gib':guest_peak/1024**3 if guest_peak is not None else None,'sample_failures':sum(len(r['sample_errors']) for r in runs)}
    report['summary']=result
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url',default='http://127.0.0.1:8081')
    parser.add_argument('--questions',default='documents/rag-questions.json')
    parser.add_argument('--profiles',default='baseline,optimized-8b,instruct-4b-q8,instruct-4b-q4')
    parser.add_argument('--repeats',type=int,default=3)
    parser.add_argument('--output',default='artifacts/optimization29')
    parser.add_argument('--resume',action='store_true')
    args=parser.parse_args();base=args.url.rstrip('/')
    if urllib.parse.urlparse(base).hostname not in ('localhost','127.0.0.1','::1'):parser.error('only local application URLs')
    if not 1<=args.repeats<=10:parser.error('repeats must be 1-10')
    out=pathlib.Path(args.output);out.mkdir(parents=True,exist_ok=True);path=out/'comparison.json'
    definitions=get(base,'/api/optimization/profiles')
    wanted=args.profiles.split(',');by_id={p['id']:p for p in definitions['profiles']}
    if len(wanted)!=len(set(wanted)) or any(p not in by_id for p in wanted):parser.error('unknown/duplicate profiles')
    profiles=[by_id[p] for p in wanted]
    installed=get(base,'/api/optimization/models');by_name={p['name'].removesuffix(':latest') if hasattr(str,'removesuffix') else p['name'].replace(':latest',''):p for p in installed}
    for p in profiles:
        if p['model'] not in by_name:raise SystemExit('Missing prepared model: '+p['model'])
    models={p['model']:by_name[p['model']] for p in profiles}
    questions=json.loads(pathlib.Path(args.questions).read_text())
    environment=get(base,'/api/status?profile='+wanted[0])
    retrieval={}
    for q in questions:
        r=get(base,'/api/rag/search?query='+urllib.parse.quote(q['query']))
        retrieval[q['id']]=digest([{'id':s['chunk_id'],'text':s['text']} for s in r['sources']])
    hardware={'host_platform':platform.platform(),'docker':command(['docker','info','--format','{{.ServerVersion}} {{.MemTotal}} {{.NCPU}}']).strip()}
    fingerprint=digest({'hardware':hardware,'questions':questions,'profiles':profiles,'models':models,'prompts':definitions['prompt_hashes'],'index':environment['index_sha256'],'retrieval':retrieval,'repeats':args.repeats,'ollama':environment['version'],'cache_mib':environment['prompt_cache_mib'],'sampler_version':1})
    pid=vm_pid()
    if path.exists():
        if not args.resume:raise SystemExit('Output exists; use --resume or a new directory')
        report=json.loads(path.read_text())
        if report['fingerprint']!=fingerprint:raise SystemExit('Resume inputs differ')
    else:
        report={'version':1,'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'complete':False,'fingerprint':fingerprint,'profiles':profiles,'models':models,'prompt_hashes':definitions['prompt_hashes'],'environment':environment,'hardware':hardware,'repeats':args.repeats,'retrieval_hashes':retrieval,'questions':[{'question':q,'runs':[]} for q in questions],'blocks':[],'method':'Profile blocks, cold warm-up separate, then each question repeated; same retrieved chunks, no history, think=false, seed42. Two-second resource samples; host metrics unavailable on Linux. No cloud calls.'}
    for profile in profiles:
        pending=[(pair,n) for pair in report['questions'] for n in range(1,args.repeats+1) if not any(r['profile']==profile['id'] and r['repeat']==n for r in pair['runs'])]
        if not pending:continue
        # Explicitly unload before a cold request. Baseline and tuned profile share weights.
        for model in models:command(['docker','compose','exec','-T','ollama','ollama','stop',model])
        block={'profile':profile['id'],'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'host_footprint_before_bytes':footprint(pid)}
        first=next(p['question'] for p in report['questions'] if p['question']['answerable'])
        block['cold']=call(base,first,profile,0,pid,retrieval[first['id']]);block['cold']['phase']='cold'
        report['blocks'].append(block);atomic(path,report)
        for pair,n in pending:
            run=call(base,pair['question'],profile,n,pid,retrieval[pair['question']['id']]);run['phase']='warm'
            pair['runs'].append(run);summarize(report);atomic(path,report)
            print(profile['id'],pair['question']['id'],n,'OK' if run['complete'] else run['error'],round(run['seconds'],2),flush=True)
        block['host_footprint_after_bytes']=footprint(pid);atomic(path,report)
    report['complete']=True;report['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();summarize(report);atomic(path,report)
    print(json.dumps(report['summary'],indent=2))
if __name__=='__main__':main()
