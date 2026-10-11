#!/usr/bin/env python3
"""Exercise the real web application and persist raw streamed responses."""
import argparse
import datetime
import json
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--url", default="http://127.0.0.1:8081")
parser.add_argument("--output", type=Path, default=Path("artifacts/web-run"))
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
results = []

def chat(identifier, messages, expected, think=False):
    payload = {"messages": messages, "think": think}
    request = urllib.request.Request(args.url.rstrip("/") + "/api/chat", data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
    started = time.monotonic()
    events = []
    with client.open(request, timeout=310) as response:
        for line in response:
            if line.strip():
                events.append(json.loads(line))
    answer = "".join(event.get("text", "") for event in events if event["type"] == "delta")
    thinking = "".join(event.get("text", "") for event in events if event["type"] == "thinking")
    failures = []
    if not events or events[-1]["type"] != "done":
        failures.append("response did not complete")
    if expected and expected.lower() not in re.sub(r"\s+", "", answer).lower():
        failures.append("expected answer missing")
    if ("<think>" in answer or "</think>" in answer or bool(thinking.strip()) != think):
        failures.append("thinking mode/channel mismatch")
    item = {"id": identifier, "request": payload, "expected": expected, "events": events, "answer": answer, "thinking": thinking, "elapsed_seconds": time.monotonic() - started, "passed": not failures, "failures": failures}
    results.append(item)
    (args.output / (identifier + ".json")).write_text(json.dumps(item, ensure_ascii=False, indent=2) + "\n")
    print(f"{identifier}: passed={item['passed']}, seconds={item['elapsed_seconds']:.2f}, delta_events={sum(event['type']=='delta' for event in events)}", flush=True)
    return item

simple = chat("01-fact", [{"role":"user","content":"Назови столицу Франции. Ответь одним словом по-русски."}], "Париж")
math_question = "Покупатель берёт 3 товара по 1200 рублей. Скидка 15% на всю сумму. Сколько он заплатит? Покажи вычисление и итог."
math = chat("02-math", [{"role":"user","content":math_question}], "3060")
chat("03-history", [{"role":"user","content":math_question},{"role":"assistant","content":math["answer"]},{"role":"user","content":"А если скидка 20%? Покажи обновлённый расчёт и итог."}], "2880")
code = chat("04-code", [{"role":"user","content":"Напиши на Go только функцию Unique(values []int) []int, без package и import. Она удаляет дубликаты, сохраняет порядок первого появления, использует map и работает за O(n) в среднем. Для nil и пустого ввода длина результата 0. Для [3, 1, 3, 2, 1] результат [3, 1, 2]."}], "Unique")
block = re.search(r"\x60{3}(?:go)?\s*\n(.*?)\x60{3}", code["answer"], re.S)
function = (block.group(1) if block else code["answer"]).strip()
tests = """package generated
import ("reflect"; "testing")
func TestUnique(t *testing.T) {
  for _,tc:=range []struct{in,want []int}{
    {[]int{3,1,3,2,1},[]int{3,1,2}},
    {nil,nil},{[]int{},nil},
    {[]int{5,5,5},[]int{5}},
    {[]int{-1,0,-1,2,0},[]int{-1,0,2}},
    {[]int{1,2,3},[]int{1,2,3}},
  }{
    got:=Unique(tc.in)
    if len(tc.want)==0 {if len(got)!=0 {t.Fatalf("expected empty, got %v",got)}} else if !reflect.DeepEqual(got,tc.want) {t.Fatalf("got %v, want %v",got,tc.want)}
  }
}
"""
validation = {"passed":False}
if function.startswith("func Unique(") and not re.search(r"\bimport\b",function):
    with tempfile.TemporaryDirectory(prefix="local-llm-generated-") as directory:
        root = Path(directory)
        (root / "go.mod").write_text("module generated\n\ngo 1.25.0\n")
        (root / "unique.go").write_text("package generated\n\n" + function + "\n")
        (root / "unique_test.go").write_text(tests)
        run = subprocess.run(["go","test","-v","./..."],cwd=root,capture_output=True,text=True,timeout=30)
        validation = {"passed":run.returncode==0,"returncode":run.returncode,"stdout":run.stdout,"stderr":run.stderr}
(args.output / "unique.go.txt").write_text(function+"\n")
(args.output / "unique_test.go.txt").write_text(tests)
(args.output / "code-validation.json").write_text(json.dumps(validation,ensure_ascii=False,indent=2)+"\n")
if not validation["passed"]:
    code["passed"] = False
    code["failures"].append("generated Go code failed validation")
    (args.output / "04-code.json").write_text(json.dumps(code,ensure_ascii=False,indent=2)+"\n")
chat("05-think", [{"role":"user","content":"Сколько будет 2 + 2? Ответь только числом."}], "4", think=True)
summary = {"base_url":args.url,"completed_at":datetime.datetime.now(datetime.timezone.utc).isoformat(),"results":results,"code_validation":validation,"failure_count":sum(not item["passed"] for item in results)}
(args.output / "results.json").write_text(json.dumps(summary,ensure_ascii=False,indent=2)+"\n")
if summary["failure_count"]:
    raise SystemExit(f"{summary['failure_count']} checks failed")
print("All five application checks passed.",flush=True)
