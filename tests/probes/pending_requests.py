# Opt-in live probe. Requires Python websockets; creates and archives a test chat.
import asyncio,json,time,argparse
parser=argparse.ArgumentParser()
parser.add_argument("--url",required=True)
parser.add_argument("--kind",choices=["input","approval"],required=True)
args=parser.parse_args()
from websockets.asyncio.client import connect
class C:
 def __init__(self):self.i=0;self.pending={};self.events=asyncio.Queue()
 async def open(self):
  self.ws=await connect(args.url,max_size=16*1024*1024);self.task=asyncio.create_task(self.read())
  await self.call('initialize',{'clientInfo':{'name':'sync_feasibility_probe','version':'1'},'capabilities':{'experimentalApi':True}})
 async def read(self):
  async for raw in self.ws:
   m=json.loads(raw)
   if 'method' not in m and m.get('id') in self.pending:self.pending.pop(m['id']).set_result(m)
   else:await self.events.put(m)
 async def call(self,m,p):
  self.i+=1;f=asyncio.get_running_loop().create_future();self.pending[self.i]=f
  await self.ws.send(json.dumps({'id':self.i,'method':m,'params':p}));r=await asyncio.wait_for(f,30)
  if 'error'in r:raise RuntimeError(r['error'])
  return r['result']
async def main():
 a=C();b=C();await a.open();await b.open();tid=None;turn=None
 try:
  r=await a.call('thread/start',{'ephemeral':False,'cwd':'/tmp','approvalPolicy':('untrusted' if args.kind=='approval' else 'on-request'),'approvalsReviewer':'user','sandbox':'read-only','baseInstructions':'This is an isolated protocol test. Follow the user prompt exactly; use only the requested harmless tool action.'})
  tid=r['thread']['id'];model=r['model']
  turn=(await a.call('turn/start',{'threadId':tid,'input':[{'type':'text','text':('Execute exactly python3 -c \"print(917*613)\" using the shell tool; do not run any other command.' if args.kind=='approval' else 'Call request_user_input now with one question: Choose A or B? Options A and B. Wait for the answer. Do not use other tools.')}],'collaborationMode':{'mode':('default' if args.kind=='approval' else 'plan'),'settings':{'model':model,'reasoning_effort':'low','developer_instructions':None}}}))['turn']['id']
  req=None;deadline=time.monotonic()+90
  while time.monotonic()<deadline:
   m=await asyncio.wait_for(a.events.get(),max(1,deadline-time.monotonic()))
   if 'id'in m and 'method'in m:
    print('writer request:',m['method'],flush=True);req=m;break
  if req:
   await b.call('thread/resume',{'threadId':tid});print('observer resume: successful',flush=True)
   seen=[]
   while True:
    try:m=await asyncio.wait_for(b.events.get(),2)
    except asyncio.TimeoutError:break
    if 'id'in m and 'method'in m:seen.append(m)
   print('observer request methods:',[m['method'] for m in seen],flush=True)
   print('request visible to subscribed second connection:',any(m['method']==req['method'] for m in seen),flush=True)
   observer=next((m for m in seen if m['method']==req['method']),None)
   if observer:
    result=({'decision':'accept'} if args.kind=='approval' else {'answers':{q['id']:{'answers':['A']} for q in observer['params'].get('questions',[])}})
    await b.ws.send(json.dumps({'id':observer['id'],'result':result}))
    deadline=time.monotonic()+30
    while time.monotonic()<deadline:
     m=await asyncio.wait_for(a.events.get(),max(1,deadline-time.monotonic()))
     if m.get('method')=='turn/completed':print('writer completed after observer answer',flush=True);turn=None;break
 finally:
  if tid and turn:
   try:await a.call('turn/interrupt',{'threadId':tid,'turnId':turn})
   except Exception as e:print('test cleanup:',type(e).__name__)
  if tid:
   try:await a.call('thread/archive',{'threadId':tid})
   except Exception as e:print('archive cleanup:',type(e).__name__)
  await a.ws.close();await b.ws.close()
  await asyncio.gather(a.task,b.task,return_exceptions=True)
asyncio.run(main())
