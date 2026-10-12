"use strict";
const make=(tag,text)=>{const e=document.createElement(tag);e.textContent=text;return e;};
(async()=>{
 const target=document.getElementById("report");
 try{
  const response=await fetch("/api/optimization/report");const r=await response.json();
  if(!response.ok)throw new Error(r.message);
  target.replaceChildren();
  target.append(make("p",r.complete?"Сравнение завершено.":"Прогон продолжается; показаны сохранённые результаты."));
  target.append(make("p",r.conclusion||"Оценка качества ещё не завершена."));
  target.append(make("p","Память контейнера и память Docker VM измерены отдельно. Холодные запросы и отказы без модели не смешиваются с генерацией."));
  const table=make("table","");const head=make("tr","");
  ["Профиль","Качество / 2","Полнота / 2","Ответ с генерацией: p50, с","Пик working set, ГиБ","Ошибки"].forEach(x=>head.append(make("th",x)));table.append(head);
  for(const p of r.profiles){
   const s=r.summary?.[p.id]||{};const row=make("tr","");
   [p.label,s.correctness?.toFixed(2)||"—",s.completeness?.toFixed(2)||"—",s.model_median_seconds?.toFixed(1)||"—",s.peak_container_gib?.toFixed(2)||"—",s.errors??"—"].forEach(x=>row.append(make("td",String(x))));table.append(row);
  }target.append(table);
  for(const q of r.questions){
   const d=make("details","");d.append(make("summary",q.question.id+" · "+q.question.query));d.append(make("p","Ожидание: "+q.question.expectations.join(" ")));
   for(const run of q.runs){
    d.append(make("h3",run.profile+" · повтор "+run.repeat+" · "+run.seconds.toFixed(1)+" с"));
    d.append(make("pre",run.text||run.error));
    if(run.grade)d.append(make("p",run.grade.rationale));
    for(const c of run.citations||[])d.append(make("pre",c.source+" · "+c.section+" · "+c.chunk_id+"\n> "+c.quote));
   }target.append(d);
  }
 }catch(e){target.textContent=e.message;}
})();