import json,sys,urllib.request,time,re
recs=[json.loads(l) for l in open('/tmp/qp/samples.jsonl')]
recs=[r for r in recs if r['messages'][0]['head'].startswith('You assess')]
for r in recs:
    b=json.loads(json.dumps(r['full'])); b['stream']=False
    raw=json.dumps(b)
    ids=sorted(set(re.findall(r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', b['messages'][1]['content'])))
    for i,u in enumerate(ids): raw=raw.replace(u,f"E{i+1}")
    b=json.loads(raw)
    t=time.time()
    d=json.load(urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8000/v1/chat/completions',data=json.dumps(b).encode(),headers={'Content-Type':'application/json'}),timeout=600))
    out=json.loads(d['choices'][0]['message']['content']); base=json.loads(r['output'])
    tm=d['timings']
    met=lambda o: ''.join('1' if v['met'] else '0' for k,v in sorted(o['criterion_assessments'].items()))
    print('alias sec',round(time.time()-t,1),'prompt',tm['prompt_n'],'(was',r['timings']['prompt_n'],') out',tm['predicted_n'],'(was',r['timings']['predicted_n'],') ready',out['ready_to_answer'],'(was',base['ready_to_answer'],') met',met(out),'was',met(base))
