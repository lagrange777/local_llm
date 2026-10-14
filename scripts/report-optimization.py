#!/usr/bin/env python3
"""Validate exact evidence and combine measured runs with explicitly reviewed grades."""
import argparse,hashlib,json,math,pathlib,statistics

def digest(x):
    return hashlib.sha256(json.dumps(x,sort_keys=True,ensure_ascii=False).encode()).hexdigest()
def raw_hash(report):
    return digest({'fingerprint':report['fingerprint'],'blocks':report['blocks'],'questions':[{'question':q['question'],'runs':[{k:v for k,v in r.items() if k not in ('grade','quotes_exact')} for r in q['runs']]} for q in report['questions']]})
def number(value,places=2):
    return '—' if value is None else f'{value:.{places}f}'
def exact(run,chunks):
    if not run['citations']:return False
    for c in run['citations']:
        chunk=chunks.get(c['chunk_id'])
        if not chunk:return False
        lines=chunk['text'].split('\n');a,b=c['start'],c['end']
        if not (1<=a<=b<=len(lines)) or c['quote']!='\n'.join(lines[a-1:b]):return False
        if any(c[k]!=chunk[k] for k in ('source','section','title')):return False
    return True
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',default='artifacts/optimization29/comparison.json')
    parser.add_argument('--reviews',default='artifacts/optimization29/reviews.json')
    args=parser.parse_args();path=pathlib.Path(args.input);r=json.loads(path.read_text());reviews=json.loads(pathlib.Path(args.reviews).read_text())
    if not r['complete'] or reviews['runs_sha256']!=raw_hash(r):raise SystemExit('Incomplete/different experiment')
    chunks={c['chunk_id']:c for c in json.load(open('documents/chunks.json'))['chunks']}
    for profile in r['profiles']:
        grades=[];runs=[]
        for pair in r['questions']:
            for run in pair['runs']:
                if run['profile']!=profile['id']:continue
                grade=reviews['questions'][pair['question']['id']][profile['id']][str(run['repeat'])]
                if grade['correctness'] not in (0,1,2) or grade['completeness'] not in (0,1,2):raise SystemExit('Invalid grade')
                if grade.get('run_sha256') and grade['run_sha256']!=digest({k:v for k,v in run.items() if k not in ('grade','quotes_exact')}):raise SystemExit('Grade refers to another run')
                run['grade']=grade;run['quotes_exact']=exact(run,chunks);grades.append(grade);runs.append(run)
        summary=r['summary'][profile['id']]
        summary.update({'correctness':statistics.mean(g['correctness'] for g in grades),'completeness':statistics.mean(g['completeness'] for g in grades),'supported_answers':sum(g['supported'] for g in grades),'answered_count':sum(a.get('status')=='answered' and a['complete'] for a in runs),'exact_citations':sum(a['quotes_exact'] for a in runs if a['complete'] and a.get('status')=='answered'),'correct_abstentions':sum(a['grade']['correctness']==2 for p in r['questions'] if not p['question']['answerable'] for a in p['runs'] if a['profile']==profile['id']),'false_abstentions':sum(a['grade']['abstention'] for p in r['questions'] if p['question']['answerable'] for a in p['runs'] if a['profile']==profile['id'])})
        answerable=[a for q in r['questions'] if q['question']['answerable'] for a in q['runs'] if a['profile']==profile['id']]
        summary['answerable_correctness']=statistics.mean(a['grade']['correctness'] for a in answerable)
        summary['answerable_completeness']=statistics.mean(a['grade']['completeness'] for a in answerable)
        first_question=next(q['question']['id'] for q in r['questions'] if q['question']['answerable'])
        first=[a['seconds'] for q in r['questions'] if q['question']['id']!=first_question for a in q['runs'] if a['profile']==profile['id'] and a['repeat']==1 and a['complete'] and a.get('metrics',{}).get('attempts',0)>0]
        repeated=[a['seconds'] for a in runs if a['repeat']>1 and a['complete'] and a.get('metrics',{}).get('attempts',0)>0]
        summary['first_pass_median_seconds']=statistics.median(first) if first else None
        summary['repeat_median_seconds']=statistics.median(repeated) if repeated else None
        all_runs=runs+[b['cold'] for b in r['blocks'] if b['profile']==profile['id']]
        summary['warm_peak_container_gib']=max((a['memory']['container_working_set_bytes']/1024**3 for a in runs if a['memory']['container_working_set_bytes'] is not None),default=None)
        for out,key in [('peak_container_gib','container_working_set_bytes'),('peak_cgroup_current_gib','container_current_bytes'),('peak_vm_guest_used_gib','vm_guest_used_bytes'),('peak_host_rss_gib','host_vm_rss_bytes'),('peak_host_compressed_gib','host_compressed_bytes'),('peak_host_swap_gib','host_swap_used_bytes')]:
            summary[out]=max((a['memory'][key]/1024**3 for a in all_runs if a['memory'].get(key) is not None),default=None)
        counters=[sample['oom_kill'] for a in all_runs for sample in a['samples']]
        summary['oom_counter_increase']=max(counters)-min(counters) if counters else None
    selection=reviews['selection'];r['selection']=selection;r['reviewer']=reviews['reviewer'];r['conclusion']=selection['rationale'];path.write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n')
    lines=['# Оптимизация локальной LLM — задание 29','',selection['rationale'],'','## Методика','',
      '10 вопросов × 3 повтора × 4 профиля = 120 попыток; дополнительно отдельный cold-запрос для каждого профиля и калибровка temperature. Каждый контрольный запрос получает одинаковые найденные чанки, без истории. think=false, seed=42, кэш промптов 512 МиБ. Основные времена относятся к прогретым весам, а не обязательно прогретому prompt.',
      '',f"Оценщик: {reviews['reviewer']}. Это оценка ассистента, не независимый человеческий аудит. Корректность/полнота 0–2; полнота относится к утверждениям ответа. Ошибки не исключены из качества. Точные цитаты не доказывают правильность смысла.",
      '',f"Окружение: {r.get('hardware',{})}. Корпус: {r['environment']['corpus_hash']}. Контекст и output-limit меняются вместе с prompt; основной эксперимент оценивает готовые профили. Сравнение двух квантований 4B сохраняет одинаковые параметры и prompt. Сравнение 8B и 4B меняет и размер/версию модели.",
      '', '## Качество, скорость и память','',
      '| Профиль | Корректность / 2 | Полнота / 2 | Ответ с генерацией p50 / p95, с | Токенов/с | Пик working set, ГиБ | Пик cgroup current, ГиБ | Пик занятой VM, ГиБ | Ошибки |',
      '|---|---:|---:|---:|---:|---:|---:|---:|---:|']
    for p in r['profiles']:
        s=r['summary'][p['id']]
        lines.append(f"| {p['id']} | {s['correctness']:.2f} | {s['completeness']:.2f} | {number(s['model_median_seconds'])} / {number(s['model_p95_seconds'])} | {number(s['median_tokens_per_second'])} | {number(s['peak_container_gib'])} | {number(s.get('peak_cgroup_current_gib'))} | {number(s.get('peak_vm_guest_used_gib'))} | {s['errors']}/{s['requests']} |")
    lines+=['','Отказы без LLM исключены из медианы. Время — полный ответ клиента в запросах с генерацией, включая поиск, проверку и ремонт; внутренний первый токен находится в исходных записях. Скорость токенов вычисляется по output tokens / eval_seconds, с учётом ремонта. Пики памяти — максимумы выборок с интервалом примерно две секунды плюс время опроса. Working set = memory.current − inactive_file; raw current включает файловый кэш. Linux VM used = MemTotal − MemAvailable. Опрос имеет накладные расходы; краткие пики могут быть пропущены.','',
      'RSS Docker VM не включает сжатую/выгруженную память. Footprint и системные compressed/swap приведены отдельно. Docker VM может сохранять выделенную память после выгрузки модели: экономия внутри контейнера не означает автоматическое уменьшение всей VM. Порядок блоков фиксирован; файловый кэш ОС не очищен.','', '## Источники и отказы','', '| Профиль | Источники с точными цитатами | Все утверждения подтверждены | Корректные отказы | Ложные отказы |','|---|---:|---:|---:|---:|']
    for p in r['profiles']:
        s=r['summary'][p['id']];lines.append(f"| {p['id']} | {s['exact_citations']}/{s['answered_count']} | {s['supported_answers']}/{s['answered_count']} | {s['correct_abstentions']}/9 | {s['false_abstentions']} |")
    lines+=['','## Ответимые вопросы и эффект кэша','','Серверные отказы на трёх неответимых вопросах одинаковы для всех профилей и не характеризуют способности LLM. Качество семи ответимых вопросов показано отдельно. Первые успешные проходы исключают q01, уже прогретый cold-запросом; повторы относятся к repeat 2–3. Ошибки не входят в медианы времени, но остаются в оценках качества и числе ошибок.','','| Профиль | Корректность / 2, ответимые | Полнота / 2, ответимые | Первый проход p50, с | Повторы p50, с | OOM |','|---|---:|---:|---:|---:|---:|']
    for p in r['profiles']:
        stats=r['summary'][p['id']]
        lines.append(f"| {p['id']} | {stats['answerable_correctness']:.2f} | {stats['answerable_completeness']:.2f} | {number(stats['first_pass_median_seconds'])} | {number(stats['repeat_median_seconds'])} | {stats['oom_counter_increase']} |")
    lines+=['','Пики памяти в основной таблице включают cold-загрузку весов и рабочие запросы; warm-only значения сохранены в JSON. Глобальные compressed/swap относятся ко всем приложениям Mac и приводятся без причинной атрибуции модели.','']
    lines+=['','## Cold-запросы и footprint Docker VM','', 'Перед cold-запросом модели выгружены через ollama stop; файловый кэш ОС остаётся. Cold-ответы не входят в контрольные 120 запросов.']
    for b in r['blocks']:
        c=b['cold'];samples=c['samples'];swap_start=samples[0].get('host_swap_used_bytes') if samples else None;swap_end=samples[-1].get('host_swap_used_bytes') if samples else None
        lines+=['',f"- {b['profile']}: cold {c['seconds']:.2f} с, завершён {c['complete']}, загрузка {c.get('metrics',{}).get('load_seconds',0):.2f} с; ошибка: {c['error'] or 'нет'}.",f"  Footprint VM до/после блока: {number(b.get('host_footprint_before_bytes')/1024**3 if b.get('host_footprint_before_bytes') is not None else None)} / {number(b.get('host_footprint_after_bytes')/1024**3 if b.get('host_footprint_after_bytes') is not None else None)} ГиБ. Системный swap в начале/конце cold: {number(swap_start/1024**3 if swap_start is not None else None)} / {number(swap_end/1024**3 if swap_end is not None else None)} ГиБ."]
    lines+=['','## Фактические модели','', '| Имя | Квантование | Размер файла, ГиБ | Digest |','|---|---|---:|---|']
    for name,m in r['models'].items():lines.append(f"| {name} | {m['details']['quantization_level']} | {m['size']/1024**3:.2f} | {m['digest']} |")
    lines+=['','## Все ответы и оценки']
    for pair in r['questions']:
        q=pair['question'];lines+=['',f"### {q['id']}: {q['query']}",'', 'Ожидание: '+' '.join(q['expectations'])]
        for run in pair['runs']:
            g=run['grade'];lines+=['',f"**{run['profile']} · повтор {run['repeat']} · {run['seconds']:.2f} с**",'',run['text'] or run['error'],'',f"Оценка {g['correctness']}/{g['completeness']}: {g['rationale']}"]
            for c in run['citations']:
                lines+=['',f"[{c['ref']}] {c['source']} · {c['section']} · chunk_id: {c['chunk_id']} · строки {c['start']}–{c['end']}",'']+['> '+x if x else '>' for x in c['quote'].split('\n')]
    lines+=['','## Артефакты','',
      '- [Все ответы, события, параметры, digest, выборки памяти](comparison.json).',
      '- [Оценки, привязанные к хешу исходных записей](reviews.json).',
      '- [Отдельный подбор temperature](tuning/comparison.json).',
      '- [Решение по калибровке](tuning/decision.json).',
      '- [Docker и окружение](environment.json).',
      '- [Чат с оптимизированным профилем](chat.jpg).',
      '- [Сравнение в интерфейсе](comparison.jpg).']
    path.with_name('REPORT.md').write_text('\n'.join(lines)+'\n')
if __name__=='__main__':main()
