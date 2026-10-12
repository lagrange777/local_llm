"use strict";
const make=(tag,text)=>{const element=document.createElement(tag);element.textContent=text;return element;};
(async()=>{
 const target=document.getElementById("report");
 try {
  const response=await fetch("/api/rag/report");const report=await response.json();
  if(!response.ok)throw new Error(report.message);
  target.replaceChildren();
  target.append(make("p",report.complete?"Прогон завершён.":"Прогон продолжается; показаны уже сохранённые ответы."));
  const note=make("p","Облачные ответы взяты из сохранённого прогона: поиск, проверка и оборудование отличаются. Это исторический ориентир, а не сравнение моделей при одинаковом контексте.");note.className="warning";target.append(note);
  target.append(make("p",report.summary_text||"Оценка ещё не завершена."));
  if(report.runtime_followup)target.append(make("p",report.runtime_followup.note));
  const table=make("table","");const head=make("tr","");
  ["Вопрос","Без RAG, с","С RAG, с","Облако ранее, с","RAG: факты"].forEach(text=>head.append(make("th",text)));table.append(head);
  for(const pair of report.questions){
   const row=make("tr","");
   const time=mode=>{const values=pair.runs.filter(run=>run.mode===mode&&run.complete).map(run=>run.seconds);values.sort((a,b)=>a-b);if(!values.length)return "—";const middle=Math.floor(values.length/2);return (values.length%2?values[middle]:(values[middle-1]+values[middle])/2).toFixed(1);};
   [pair.question.id+": "+pair.question.query,time("plain"),time("rag"),pair.cloud?(pair.cloud.duration_ms/1000).toFixed(1):"—",pair.review?.rag||"Не оценено"].forEach(text=>row.append(make("td",text)));table.append(row);
  }
  target.append(table);
  for(const pair of report.questions){
   const details=make("details","");details.append(make("summary",pair.question.id+" · "+pair.question.query));
   details.append(make("p","Ожидание: "+pair.question.expectations.join(" ")));
   if(pair.review)details.append(make("p","Оценка: "+JSON.stringify(pair.review)));
   for(const run of pair.runs){details.append(make("h3",(run.mode==="rag"?"С RAG":"Без RAG")+" · прогон "+run.repeat));details.append(make("pre",run.text||run.error));if(run.grade)details.append(make("p",run.grade.rationale));
    for(const citation of run.citations||[])details.append(make("pre",citation.source+" · "+citation.section+" · "+citation.chunk_id+"\n> "+citation.quote));
   }
   if(pair.cloud){details.append(make("h3","Облачный ответ из предыдущего задания"));details.append(make("pre",pair.cloud.text));}
   target.append(details);
  }
 }catch(error){target.textContent=error.message||"Не удалось загрузить отчёт.";}
})();
