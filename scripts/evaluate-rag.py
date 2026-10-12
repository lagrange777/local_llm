#!/usr/bin/env python3
"""Measure local chat with/without BM25; reuse saved cloud answers, no paid API calls."""
import argparse
import datetime
import hashlib
import json
import pathlib
import statistics
import time
import urllib.request
import urllib.error
import urllib.parse


def atomic_json(path, value):
    path = pathlib.Path(path)
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    temporary.replace(path)


def call(base, question, mode, repeat):
    body = {'messages': [{'role': 'user', 'content': question}], 'think': False, 'rag': mode == 'rag', 'profile':'baseline'}
    request = urllib.request.Request(base + '/api/chat', data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
    run = {'mode': mode, 'repeat': repeat, 'request': body, 'complete': False, 'text': '', 'citations': [], 'events': [], 'error': None, 'first_answer_seconds': None}
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=360) as response:
            for line in response:
                event = json.loads(line)
                run['events'].append(event)
                if event['type'] == 'delta':
                    if run['first_answer_seconds'] is None:
                        run['first_answer_seconds'] = time.perf_counter() - started
                    run['text'] += event.get('text', '')
                elif event['type'] == 'retrieval':
                    run['retrieval'] = event['retrieval']
                elif event['type'] == 'sources':
                    run['citations'] = event['answer']['citations']
                    run['claims'] = event['answer']['claims']
                    run['status'] = event['answer']['status']
                elif event['type'] == 'done':
                    run['complete'] = True
                    run['metrics'] = event
                elif event['type'] == 'error':
                    run['error'] = event.get('text', '')
                    run['error_code'] = event.get('code', '')
        if not run['complete'] and not run['error']:
            run['error'] = 'Stream ended without done'
    except (OSError, ValueError, urllib.error.HTTPError) as error:
        run['error'] = str(error)
    run['seconds'] = time.perf_counter() - started
    return run


def summarize(report):
    summary = {}
    for mode in ['plain', 'rag']:
        runs = [r for q in report['questions'] for r in q['runs'] if r['mode'] == mode]
        complete = [r for r in runs if r['complete']]
        model_runs = [r for r in complete if r.get('metrics', {}).get('attempts', 0) > 0]
        times = sorted(r['seconds'] for r in complete)
        model_times = sorted(r['seconds'] for r in model_runs)
        summary[mode] = {'requests': len(runs), 'completed': len(complete), 'errors': len(runs) - len(complete), 'median_seconds': statistics.median(times) if times else None, 'p95_seconds': times[min(len(times)-1, int(len(times)*0.95))] if times else None, 'model_requests': len(model_runs), 'median_model_request_seconds': statistics.median(model_times) if model_times else None, 'server_abstentions': len(complete)-len(model_runs), 'repair_requests': sum(r.get('metrics', {}).get('attempts', 0) > 1 for r in model_runs), 'failed_repairs': sum(r.get('error_code') == 'invalid_evidence' for r in runs)}
        summary[mode]['median_first_answer_seconds'] = statistics.median([r['first_answer_seconds'] for r in complete if r['first_answer_seconds'] is not None]) if complete else None
        summary[mode]['median_model_first_token_seconds'] = statistics.median([r['metrics']['model_first_token_seconds'] for r in model_runs]) if model_runs else None
        hashes = []
        for question in report['questions']:
            qruns = [r for r in question['runs'] if r['mode'] == mode and r['complete']]
            if len(qruns) == report['repeats']:
                hashes.append(len(set(r['text'] for r in qruns)) == 1)
        summary[mode]['identical_answers_questions'] = sum(hashes)
        summary[mode]['fully_repeated_questions'] = len(hashes)
    report['summary'] = summary
    report['summary_text'] = ' · '.join(f"{('С RAG' if mode == 'rag' else 'Без RAG')}: {s['completed']}/{s['requests']} завершены, медиана {s['median_seconds'] or 0:.1f} с" for mode, s in summary.items())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='http://127.0.0.1:8081')
    parser.add_argument('--questions', default='documents/rag-questions.json')
    parser.add_argument('--cloud', default='documents/cloud-baseline.json')
    parser.add_argument('--output', default='artifacts/rag28')
    parser.add_argument('--repeats', type=int, default=3)
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    if not 1 <= args.repeats <= 10:
        parser.error('repeats must be 1–10')
    base = args.url.rstrip('/')
    if urllib.parse.urlparse(base).hostname not in ('localhost', '127.0.0.1', '::1'):
        parser.error('benchmark only supports a local application URL')
    out = pathlib.Path(args.output); out.mkdir(parents=True, exist_ok=True)
    path = out / 'comparison.json'
    raw = pathlib.Path(args.questions).read_bytes()
    questions = json.loads(raw)
    cloud = json.loads(pathlib.Path(args.cloud).read_text())
    with urllib.request.urlopen(base + '/api/status?profile=baseline', timeout=10) as response:
        environment = json.load(response)
    if not environment.get('ready') or not environment.get('rag_ready'):
        raise SystemExit('Model and RAG index must both be ready')
    if cloud['corpus_hash'] != environment['corpus_hash']:
        raise SystemExit('Cloud baseline corpus differs from local corpus')
    fingerprint = hashlib.sha256(json.dumps({'dataset': hashlib.sha256(raw).hexdigest(), 'environment': environment, 'repeats': args.repeats, 'generation': {'think': False, 'temperature': 0, 'seed': 42, 'num_ctx': 8192, 'num_predict': 1024}}, sort_keys=True).encode()).hexdigest()
    if args.resume and path.exists():
        report = json.loads(path.read_text())
        if report['fingerprint'] != fingerprint:
            raise SystemExit('Resume settings differ; use a new output directory')
    else:
        if path.exists():
            raise SystemExit('Output already exists; use --resume or a new output directory')
        saved = {item['id']: item for item in cloud['answers']}
        report = {'version': 1, 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'complete': False, 'fingerprint': fingerprint, 'environment': environment, 'repeats': args.repeats, 'generation': {'think': False, 'temperature': 0, 'seed': 42, 'num_ctx': 8192, 'num_predict': 1024}, 'cloud_provenance': {k:v for k,v in cloud.items() if k != 'answers'}, 'questions': [{'question': q, 'cloud': saved.get(q['id']), 'runs': []} for q in questions]}
    report.setdefault('execution_order_note', 'Pairs repeated per question; mode order alternates by repeat. Configured Ollama prompt cache is retained.')
    for pair in report['questions']:
        for repeat in range(1, args.repeats + 1):
            for mode in (['plain', 'rag'] if repeat % 2 else ['rag', 'plain']):
                if any(r['repeat'] == repeat and r['mode'] == mode for r in pair['runs']):
                    continue
                run = call(base, pair['question']['query'], mode, repeat)
                pair['runs'].append(run)
                summarize(report)
                atomic_json(path, report)
                print(f"{pair['question']['id']} {mode} repeat={repeat}: {'done' if run['complete'] else run['error']} {run['seconds']:.2f}s", flush=True)
    report['complete'] = True
    report['completed_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    summarize(report);atomic_json(path, report)
    print(report['summary_text'], flush=True)


if __name__ == '__main__':
    main()
