"use strict";
const $ = (id) => document.getElementById(id);
const storageKey = "local-llm-chat-v1";
let turns = [];
let controller = null;
let ready = false;
let refreshTimer;
const welcome = $("welcome");
function notice(message) { $("notice").textContent = message || ""; $("notice").hidden = !message; }
function save() {
  try { localStorage.setItem(storageKey, JSON.stringify({turns:turns.slice(-100),think:$("think").checked})); }
  catch (_) { notice("История не сохранилась: браузер не разрешает запись или хранилище заполнено."); }
}
function restore() {
  try {
    const data = JSON.parse(localStorage.getItem(storageKey) || "{}");
    if (Array.isArray(data.turns)) {
      turns = data.turns.filter(t => t && typeof t.question === "string" && typeof t.content === "string" && t.question.length <= 8000 && t.content.length <= 100000).slice(-100);
      for (const turn of turns) {
        if (!["complete","error","stopped"].includes(turn.state)) { turn.state="stopped"; turn.error="Генерация была прервана."; }
        if (typeof turn.thinking!=="string") turn.thinking="";
      }
    }
    $("think").checked = data.think === true;
  } catch (_) { turns=[]; }
}
function titles() {
  const title = turns.length ? turns[0].question.slice(0,48) : "Новый диалог";
  $("dialog-title").textContent=title;
  $("sidebar-title").textContent=title;
  $("turn-count").textContent=turns.length;
  welcome.hidden=turns.length>0;
}
function node(tag, className, text) {
  const el=document.createElement(tag);
  if (className) el.className=className;
  if (text!==undefined) el.textContent=text;
  return el;
}
function appendInline(element, text) {
  const markup=/\*\*([^*]+)\*\*|\x60([^\x60\n]+)\x60/g;
  let start=0,match;
  while ((match=markup.exec(text))!==null) {
    if (match.index>start) element.append(node("span","",text.slice(start,match.index)));
    element.append(node(match[1]!==undefined?"strong":"code","",match[1]||match[2]));
    start=markup.lastIndex;
  }
  if (start<text.length) element.append(node("span","",text.slice(start)));
}
function answerMarkup(element, text) {
  element.replaceChildren();
  if (/^(?:func\s+\w+\s*\(|package\s+\w+\s*\n)/.test(text.trim())) {
    const pre=node("pre"); pre.append(node("code","",text.trim())); element.append(pre); return;
  }
  const fence = /```(?:[a-zA-Z0-9_+-]*)\n([\s\S]*?)```/g;
  let start=0, match;
  while ((match=fence.exec(text))!==null) {
    if (match.index>start) appendInline(element,text.slice(start,match.index));
    const pre=node("pre"); pre.append(node("code","",match[1].trimEnd())); element.append(pre);
    start=fence.lastIndex;
  }
  if (start<text.length) appendInline(element,text.slice(start));
}
function renderTurn(turn) {
  const container=node("section","turn");
  const user=node("div","message user"); user.append(node("div","avatar","Вы"));
  const userBody=node("div","message-content"); userBody.append(node("div","message-label","Вы"),node("div","question",turn.question)); user.append(userBody);
  const assistant=node("div","message assistant"); assistant.append(node("div","avatar","✳"));
  const body=node("div","message-content"); body.append(node("div","message-label","Local"));
  const thinking=node("details","thinking"); thinking.hidden=!turn.thinking;
  thinking.append(node("summary","","Рассуждения модели"));
  const thought=node("pre","",turn.thinking); thinking.append(thought);
  const answer=node("div","answer"); answerMarkup(answer,turn.content);
  const pending=node("div","pending-label",turn.thinking ? "Формулирует ответ" : "Модель отвечает"); pending.hidden=turn.state!=="pending";
  const meta=node("div","message-meta"); meta.hidden=true;
  const error=node("div","message-error",turn.error||""); error.hidden=!turn.error;
  body.append(thinking,answer,pending,meta,error); assistant.append(body); container.append(user,assistant);
  $("conversation").append(container);
  const view={thinking,thought,answer,pending,meta,error};
  updateTurn(turn,view);
  return view;
}
function updateTurn(turn, view) {
  view.thinking.hidden=!turn.thinking;
  view.thought.textContent=turn.thinking;
  view.answer.textContent=turn.content;
  if (turn.state==="complete") answerMarkup(view.answer,turn.content);
  view.pending.hidden=turn.state!=="pending";
  view.pending.textContent=turn.thinking ? "Формулирует ответ" : "Модель отвечает";
  view.error.textContent=turn.error||""; view.error.hidden=!turn.error;
  view.meta.hidden=turn.state!=="complete";
  if (turn.state==="complete") view.meta.textContent=(Number(turn.seconds)||0).toFixed(1)+" с · "+(Number(turn.tokens)||0)+" токенов · "+(turn.think?"С рассуждениями":"Без рассуждений")+(turn.trimmed?" · Ранние сообщения не вошли в контекст":"");
}
function scrollDown() {
  const messages=$("messages");
  messages.scrollTop=messages.scrollHeight;
}
function busy(value) {
  $("send").hidden=value; $("stop").hidden=!value;
  $("prompt").disabled=value; $("new-chat").disabled=value; $("think").disabled=value;
  $("send").disabled=!ready;
  $("composer-hint").textContent=value?"Ответ генерируется на локальной модели":"Enter — отправить · Shift + Enter — новая строка";
}
async function refresh() {
  clearTimeout(refreshTimer);
  try {
    const response=await fetch("/api/status",{signal:AbortSignal.timeout(7000)});
    const status=await response.json();
    ready=response.ok && status.ready;
    $("model").textContent=status.model||"Qwen3";
    $("status-label").textContent=ready?"Доступна":"Недоступна";
    $("status-dot").className="status-dot "+(ready?"ready":"offline");
    if (!ready) notice(status.message||"Не удалось подключиться к локальной модели.");
    else if ($("notice").textContent.includes("Ollama") || $("notice").textContent.includes("Модель не установлена")) notice("");
  } catch (_) {
    ready=false; $("status-label").textContent="Нет связи"; $("status-dot").className="status-dot offline";
    notice("Нет связи с приложением. Проверьте, что сервер запущен.");
  }
  $("send").disabled=!ready;
  refreshTimer=setTimeout(refresh,30000);
}
async function sendQuestion(event) {
  event.preventDefault();
  if (controller || !ready) return;
  const question=$("prompt").value.trim();
  if (!question) return;
  const history=turns.filter(t=>t.state==="complete").slice(-10).flatMap(t=>[{role:"user",content:t.question},{role:"assistant",content:t.content}]);
  history.push({role:"user",content:question});
  let contextSize=history.reduce((size,message)=>size+message.content.length,0);
  let trimmed=turns.filter(t=>t.state==="complete").length>10;
  while (contextSize>6000 && history.length>1) {
    contextSize-=history[0].content.length+history[1].content.length; history.splice(0,2); trimmed=true;
  }
  const turn={question,content:"",thinking:"",state:"pending",think:$("think").checked,trimmed};
  turns.push(turn); titles(); save();
  const view=renderTurn(turn);
  controller=new AbortController();
  const activeController=controller;
  busy(true); notice(""); $("prompt").value=""; scrollDown();
  let finished=false;
  function receive(item) {
    if (item.type==="start") turn.trimmed=turn.trimmed || item.history_trimmed===true;
    else if (item.type==="delta") turn.content+=item.text||"";
    else if (item.type==="thinking") turn.thinking+=item.text||"";
    else if (item.type==="done") { finished=true; turn.state="complete"; turn.seconds=item.seconds; turn.tokens=item.tokens; }
    else if (item.type==="error") throw new Error(item.text||"Не удалось получить ответ.");
    updateTurn(turn,view); scrollDown();
  }
  try {
    const response=await fetch("/api/chat",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({messages:history,think:turn.think}),signal:activeController.signal});
    if (!response.ok) { const error=await response.json(); throw new Error(error.message||"Не удалось отправить сообщение."); }
    if (!response.body) throw new Error("Браузер не поддерживает потоковый ответ.");
    const reader=response.body.getReader(); const decoder=new TextDecoder(); let buffer="";
    try {
      for (;;) {
        const chunk=await reader.read();
        buffer+=decoder.decode(chunk.value||new Uint8Array(),{stream:!chunk.done});
        let newline;
        while ((newline=buffer.indexOf("\n"))!==-1) {
          const line=buffer.slice(0,newline).trim(); buffer=buffer.slice(newline+1);
          if (line) receive(JSON.parse(line));
        }
        if (chunk.done) break;
      }
      if (buffer.trim()) receive(JSON.parse(buffer));
    } finally { reader.releaseLock(); }
    if (!finished) throw new Error("Поток оборвался до завершения ответа. Повторите запрос.");
  } catch (error) {
    if (activeController.signal.aborted) { turn.state="stopped"; turn.error="Генерация остановлена. Этот обмен не включён в контекст следующих вопросов."; }
    else { turn.state="error"; turn.error=error.message||"Ошибка запроса."; activeController.abort(); }
  } finally {
    updateTurn(turn,view); save(); controller=null; busy(false); $("prompt").focus(); scrollDown();
  }
}
$("chat-form").addEventListener("submit",sendQuestion);
$("prompt").addEventListener("keydown",event=>{
  if (event.key==="Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); $("chat-form").requestSubmit(); }
});
$("stop").addEventListener("click",()=>controller?.abort());
$("new-chat").addEventListener("click",()=>{
  if (controller) return;
  turns=[]; $("conversation").replaceChildren(); $("prompt").value=""; titles(); save(); notice(""); $("prompt").focus();
});
$("think").addEventListener("change",save);
$("connection").addEventListener("click",refresh);
document.querySelectorAll("[data-prompt]").forEach(button=>button.addEventListener("click",()=>{$("prompt").value=button.dataset.prompt; $("prompt").focus();}));
restore(); turns.forEach(renderTurn); titles(); busy(false); refresh(); scrollDown();
