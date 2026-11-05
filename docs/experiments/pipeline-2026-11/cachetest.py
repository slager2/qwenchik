import json,sys,urllib.request,time
URL=sys.argv[1]
recs=[json.loads(l) for l in open('/tmp/qp/samples.jsonl')]
ver=[r for r in recs if r['messages'][0]['head'].startswith('You assess')]
pla=[r for r in recs if r['messages'][0]['head'].startswith('You are the planner')]
def call(msgs, tag, schema=None, max_tokens=16):
    b={"model":"local-qwen","messages":msgs,"max_tokens":max_tokens,"temperature":0.2,"top_p":0.8,"cache_prompt":True,"chat_template_kwargs":{"enable_thinking":False}}
    t=time.time()
    d=json.load(urllib.request.urlopen(urllib.request.Request(URL+'/v1/chat/completions',data=json.dumps(b).encode(),headers={'Content-Type':'application/json'}),timeout=900))
    tm=d['timings']; print(f"{tag:40s} prompt_n={tm['prompt_n']:5d} cache_n={tm['cache_n']:5d} prompt_ms={tm['prompt_ms']:.0f}", flush=True)
    return d['choices'][0]['message']['content']
m=lambda r:[{"role":x["role"],"content":x["content"]} for x in r['full']['messages']]
v1,v2,p1,p2=m(ver[0]),m(ver[1]),m(pla[0]),m(pla[1])
call(p1,'planner A (cold)')
call(p1,'planner A again (resume/retry)')
out=call(p1,'planner A third')
call(p1+[{"role":"assistant","content":"{\"bad\":1}"},{"role":"user","content":"{\"validation_error\":\"x\",\"instruction\":\"fix\"}"}],'planner A repair continuation')
call(p2,'planner B (same system, other user)')
call(v1,'verifier A (role switch)')
call(p1,'planner A after verifier (ram cache?)')
call(v2,'verifier B (same system)')
