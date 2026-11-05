import json,sys
from collections import Counter
recs=[json.loads(l) for l in open(sys.argv[1])]
if len(sys.argv)>3:
    t0,t1=float(sys.argv[2]),float(sys.argv[3]); recs=[r for r in recs if t0<=r['t']<=t1]
print(Counter(r['path'] for r in recs))
tot=Counter()
for r in recs:
    if r['path'].endswith('chat/completions'):
        m=r['messages']; t=r.get('timings',{})
        tot['calls']+=1; tot['prompt']+= (t.get('prompt_n') or 0)+(t.get('cache_n') or 0); tot['cached']+=t.get('cache_n') or 0; tot['out']+=t.get('predicted_n') or 0; tot['sec']+=r['sec']
        print(round(r['sec'],1), r['params'], [ (x['role'],x['len']) for x in m], 'p',t.get('prompt_n'),'c', t.get('cache_n'),'o', t.get('predicted_n'), '|', m[0]['head'][:50].replace('\n',' '))
print(dict(tot))
