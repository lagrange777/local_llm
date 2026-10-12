#!/usr/bin/env python3
"""Validate citations and combine explicit reviewer grades with measured runs."""
import argparse
import hashlib
import json
import pathlib
import statistics


def runs_hash(report):
    payload = {'environment': report['environment'], 'generation': report['generation'], 'questions': [{'question': p['question'], 'runs': [{k:v for k,v in r.items() if k not in ('grade','quotes_exact')} for r in p['runs']]} for p in report['questions']]}
    return hashlib.sha256(json.dumps(payload, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


def normalized(text):
    return ' '.join(text.split())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', default='artifacts/rag28/comparison.json')
    parser.add_argument('--reviews', default='artifacts/rag28/reviews.json')
    parser.add_argument('--index', default='documents/chunks.json')
    args = parser.parse_args()
    path = pathlib.Path(args.input)
    report = json.loads(path.read_text())
    reviews = json.loads(pathlib.Path(args.reviews).read_text())
    if not report['complete'] or reviews['runs_sha256'] != runs_hash(report):
        raise SystemExit('Incomplete run or reviewer grades refer to different outputs')
    chunks = {c['chunk_id']: c for c in json.loads(pathlib.Path(args.index).read_text())['chunks']}
    statistics_by_mode = {}
    for mode in ['plain', 'rag']:
        grades, complete_runs, answerable_sources, quote_checks, retrieval_times, expected_recalls = [], [], [], [], [], []
        for pair in report['questions']:
            question = pair['question']
            for run in pair['runs']:
                if run['mode'] != mode:
                    continue
                grade = reviews['questions'][question['id']][mode][str(run['repeat'])]
                if grade['correctness'] not in (0,1,2) or grade['completeness'] not in (0,1,2):
                    raise SystemExit('Invalid reviewer grade')
                run['grade'] = grade
                grades.append(grade)
                complete_runs.append(run)
                citations = run['citations']
                exact = True
                for citation in citations:
                    chunk = chunks.get(citation['chunk_id'])
                    if not chunk:
                        exact = False; continue
                    lines = chunk['text'].split('\n')
                    start,end=citation['start'],citation['end']
                    exact = exact and 1 <= start <= end <= len(lines) and citation['quote'] == '\n'.join(lines[start-1:end]) and all(citation[k] == chunk[k] for k in ('source','section','title'))
                run['quotes_exact'] = bool(citations) and exact
                if mode == 'rag':
                    selected=run.get('retrieval',{}).get('sources',[])
                    if question['answerable']:
                        expected_recalls.append(any(e['source']==s['source'] and normalized(e['quote']) in normalized(s['text']) for e in question['relevant'] for s in selected))
                    if run['complete'] and run.get('status') == 'answered':
                        answerable_sources.append(bool(citations));quote_checks.append(run['quotes_exact'])
                    retrieval_times.append(run.get('retrieval',{}).get('duration_ms',0))
            per_question=list(reviews['questions'][question['id']][mode].values())
            pair.setdefault('review',{})[mode]=f"{statistics.mean(g['correctness'] for g in per_question):.2f}/2, полнота {statistics.mean(g['completeness'] for g in per_question):.2f}/2"
        statistics_by_mode[mode]={'mean_correctness':statistics.mean(g['correctness'] for g in grades),'mean_completeness':statistics.mean(g['completeness'] for g in grades),'correct_unanswerable':sum(not p['question']['answerable'] and r['grade']['correctness']==2 for p in report['questions'] for r in p['runs'] if r['mode']==mode),'false_abstentions':sum(p['question']['answerable'] and r['grade']['abstention'] for p in report['questions'] for r in p['runs'] if r['mode']==mode),'sources_present_answered':sum(answerable_sources),'answered_count':len(answerable_sources),'quotes_exact_answered':sum(quote_checks),'all_claims_supported_answered':sum(r['grade']['supported'] for p in report['questions'] for r in p['runs'] if r['mode']==mode and r['complete'] and r.get('status')=='answered'),'expected_quote_retrieved':sum(expected_recalls),'answerable_retrievals':len(expected_recalls),'median_retrieval_ms':statistics.median(retrieval_times) if retrieval_times else None,'fully_correct_repeated_questions':sum(len(rs)==report['repeats'] and all(r['complete'] and r['grade']['correctness']==2 and r['grade']['completeness']==2 for r in rs) for p in report['questions'] for rs in [[r for r in p['runs'] if r['mode']==mode]])}
    report['quality']=statistics_by_mode
    report['reviewer']=reviews['reviewer']
    report['summary_text']=report['summary_text'].split(' \u00b7 \u041e\u0446\u0435\u043d\u043a\u0430')[0]
    report['summary_text']+=' · Оценка корректности: '+', '.join(f"{mode} {s['mean_correctness']:.2f}/2" for mode,s in statistics_by_mode.items())
    follow_path=path.parent/'cache-fixed'/'comparison.json'
    cold_path=path.parent/'cold-warm.json'
    if follow_path.exists() and cold_path.exists():
        follow=json.loads(follow_path.read_text());cold=json.loads(cold_path.read_text())
        report['runtime_followup']={'cache_mib':512,'summary':follow['summary'],'cold_warm':cold,'note':'Исходный прогон: два сбоя, включая OOM Ollama. Финальный кэш ограничен до 512 МиБ; отдельный повтор q09: '+str(sum(s['completed'] for s in follow['summary'].values()))+'/6 завершены. Это ограниченная повторная проверка, а не новый прогон всех вопросов.'}
    path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    lines=['# Локальная LLM + BM25 RAG: результаты задания 28','',report['summary_text'],'','## Методика','',f"10 вопросов × 3 повтора × 2 режима. Модель {report['environment']['model']}, Ollama {report['environment']['version']}, temperature=0, seed=42, think=false, контекст 8192, выход до 1024 токенов. Вопросы без истории; порядок режимов чередуется. В каждом запросе сохраняются события API и время клиента.",'',f"Оценщик: {reviews['reviewer']}. Оценка сделана по ожиданиям и цитатам после просмотра всех ответов; полнота оценивает утверждения ответа, а не дополнительные факты только в карточке цитаты; это не независимый человеческий аудит. Шкала 0–2: 0 — неверно/ответа нет, 1 — частично, 2 — корректно/полно. Ошибки API не исключаются из оценки качества.",'','## Сводка','', '| Метрика | Без RAG | BM25 + Qwen |','|---|---:|---:|']
    a,b=report['summary']['plain'],report['summary']['rag'];qa,qb=statistics_by_mode['plain'],statistics_by_mode['rag']
    for title,x,y in [('Завершённые запросы',f"{a['completed']}/{a['requests']}",f"{b['completed']}/{b['requests']}"),('Ошибки',a['errors'],b['errors']),('Средняя корректность / 2',f"{qa['mean_correctness']:.2f}",f"{qb['mean_correctness']:.2f}"),('Средняя полнота / 2',f"{qa['mean_completeness']:.2f}",f"{qb['mean_completeness']:.2f}"),('Правильные отказы на 3 неответимых вопросах × 3',qa['correct_unanswerable'],qb['correct_unanswerable']),('Ложные отказы на ответимых вопросах',qa['false_abstentions'],qb['false_abstentions']),('Медиана полного ответа, с',f"{a['median_seconds']:.2f}",f"{b['median_seconds']:.2f}"),('p95 полного ответа, с',f"{a['p95_seconds']:.2f}",f"{b['p95_seconds']:.2f}"),('Медиана запроса с вызовом модели, с',f"{a['median_model_request_seconds']:.2f}",f"{b['median_model_request_seconds']:.2f}"),('Медиана первого видимого ответа, с',f"{a['median_first_answer_seconds']:.2f}",f"{b['median_first_answer_seconds']:.2f}"),('Медиана первого внутреннего токена модели, с',f"{a['median_model_first_token_seconds']:.2f}",f"{b['median_model_first_token_seconds']:.2f}"),('Вопросы с идентичными текстами в 3 повторах',f"{a['identical_answers_questions']}/10",f"{b['identical_answers_questions']}/10"),('Успешные исправления JSON/цитат',a['repair_requests'],b['repair_requests']),('Неудачные исправления JSON/цитат',a.get('failed_repairs',0),b.get('failed_repairs',0)),('Вопросы с полным верным ответом во всех 3 повторах',qa['fully_correct_repeated_questions'],qb['fully_correct_repeated_questions']),('Отказы без вызова модели',a['server_abstentions'],b['server_abstentions'])]:lines.append(f'| {title} | {x} | {y} |')
    lines+=['',f"Поиск BM25: медиана {qb['median_retrieval_ms']:.3f} мс. Ожидаемый фрагмент найден в {qb['expected_quote_retrieved']}/{qb['answerable_retrievals']} ответимых поисках. У ответов RAG: источники {qb['sources_present_answered']}/{qb['answered_count']}, точные цитаты {qb['quotes_exact_answered']}/{qb['answered_count']}, смысл всех утверждений подтверждён оценщиком {qb['all_claims_supported_answered']}/{qb['answered_count']}.",'','Отказы сервера не требуют вызова LLM и заметно уменьшают общую медиану RAG. Поэтому отдельно приведено время запросов с генерацией. RAG показывает ответ после проверки структуры; внутренний первый токен не является видимым ответом. Исходные 60 запросов выполнялись на CPU Docker Desktop / Apple M1 Pro с прогретыми весами и стандартным кэшем промптов Ollama (8192 МиБ); в ходе прогона был OOM. Финальная конфигурация ограничивает кэш до 512 МиБ. Повтор после исправления и холодный запуск приведены отдельно; исходные метрики не подменены. Три повтора при температуре 0 — ограниченная проверка стабильности.','', '## По вопросам','', '| Вопрос | Без RAG: корректность/полнота | RAG: корректность/полнота | Примечание |','|---|---|---|---|']
    for pair in report['questions']:
        q=pair['question'];note=reviews['questions'][q['id']].get('note','')
        lines.append(f"| {q['id']} | {pair['review']['plain']} | {pair['review']['rag']} | {note.replace('|','/')} |")
    cloud_times=[p['cloud']['duration_ms']/1000 for p in report['questions'] if p.get('cloud')]
    lines+=['','## Облачный исторический ориентир','', f"Сохранённые ответы {report['cloud_provenance']['model']} от {report['cloud_provenance']['created_at']}; медиана сохранённой генерации и проверок {statistics.median(cloud_times):.2f} с. Старый отчёт оценивал корректность и полноту в 2/2 на всех 10 вопросах, семь ответимых ответов содержали точные цитаты, три отказа были корректны.",'','Это другой эксперимент: OpenAI-эмбеддинги, LLM-реранкер, другие чанки и семантический проверяющий. Время не включает общий поиск и финального оценщика; оборудование облака неизвестно. Нельзя приписать различие только модели, считать прямое отношение скоростей или оценить облачную стабильность по одному прогону. Новые платные API-вызовы не выполнялись.','']
    if report.get('runtime_followup'):
        f=report['runtime_followup']
        lines+=['','## Память Ollama и повтор после исправления','',f['note'],'']
        lines+=['Исходный OOM подтверждён memory.events (oom_kill=1) и логом остановки runner при сохранении состояния промпта. В финальном Compose задан LLAMA_ARG_CACHE_RAM=512; фактический предел подтверждён логом llama-server. Базовая память весов и KV остаётся значительной. См. cache-before.json, cache-before.txt и cache-after.json.','']
        for mode,stats in f['summary'].items():
            lines.append(f"- Повтор q09 {mode}: {stats['completed']}/{stats['requests']}, ошибок {stats['errors']}; медиана {stats['median_seconds']:.2f} с.")
        for name,run in f['cold_warm']['runs'].items():
            lines.append(f"- {name}: полный ответ {run['seconds']:.2f} с; загрузка весов {run.get('metrics',{}).get('load_seconds',0):.2f} с; завершён {run['complete']}.")
        lines+=['','Для холодного запроса q03 выполнено ollama stop qwen3:8b, затем сразу два RAG-запроса без истории; файловый кэш ОС не очищался. Оба ответа: 1 МиБ, с точной цитатой ARCHITECTURE.md. Ограничение кэша уменьшает риск OOM, но может уменьшить ускорение повторов. Шесть запросов q09 и два холодный/тёплый не доказывают отсутствие ошибок во всех сценариях. Исходные 60 запросов сохранены без замены неудачных записей.']
    lines+=['','## Исходные ответы и основания оценки']
    for pair in report['questions']:
        q=pair['question'];lines+=['',f"### {q['id']}: {q['query']}",'','Ожидание: '+' '.join(q['expectations']),'','Ожидаемые источники: '+(', '.join(e['source'] for e in q['relevant']) or 'нет факта в корпусе')]
        for mode in ['plain','rag']:
            runs=sorted([r for r in pair['runs'] if r['mode']==mode],key=lambda r:r['repeat'])
            lines+=['',f"**{'С RAG' if mode=='rag' else 'Без RAG'}**"]
            for run in runs:
                lines+=['',f"Повтор {run['repeat']}, {run['seconds']:.2f} с: {run['text'] or run['error']}",'','Оценка: '+run['grade']['rationale']]
                for citation in run['citations']:
                    lines+=['',f"[{citation['ref']}] {citation['source']} · {citation['section']} · chunk_id: `{citation['chunk_id']}` · строки {citation['start']}–{citation['end']}",'']+['> '+line if line else '>' for line in citation['quote'].split('\n')]
        if pair.get('cloud'):lines+=['','**Облачный ответ из старого отчёта**','',pair['cloud']['text']]
    lines+=['','## Артефакты','', '- [Полные события, ответы, метрики и оценки](comparison.json).','- [Оценки и привязка к хешу прогона](reviews.json).','- [Отдельная калибровка поиска](calibration.json).','- [Повтор q09 после ограничения кэша](cache-fixed/comparison.json).','- [Диагностика OOM](cache-before.json).','- [Проверка ограниченного кэша](cache-after.json).','- [Холодный и прогретый запросы](cold-warm.json).','- [Проверка изоляции сети и Docker-окружение](environment.json).','- [Финальное Docker-окружение](environment-final.json).','- [Интерфейс с источником и цитатой](chat.jpg).','- [Отказ при слабом контексте](unknown.jpg).','- [Сравнение в веб-интерфейсе](comparison.jpg).']
    path.with_name('REPORT.md').write_text('\n'.join(lines)+'\n')


if __name__ == '__main__':
    main()
