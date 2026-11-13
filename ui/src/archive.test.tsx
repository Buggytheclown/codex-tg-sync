// @vitest-environment jsdom
import {act,cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react";
import {afterEach,expect,it,vi} from "vitest";
import {App} from "./App";
import {compactTask,emptyConfig,type Task} from "./model";
afterEach(()=>{cleanup();vi.unstubAllGlobals();vi.useRealTimers();window.localStorage.clear();window.history.replaceState(null,"","/")});
const task=(id:string,column:Task["column"]):Task=>({id,title:id,column,description:"",position:0,thread_id:"",source_url:"",source_key:"",config:emptyConfig(),version:1,created_at:"",updated_at:"",requests:[]});
it("loads Done explicitly and never refetches its list or selected detail on background signals",async()=>{
 vi.useFakeTimers({toFake:["setInterval","clearInterval"]});
 let invalidate=()=>{};let stream!:{onopen:null|(()=>void);onerror:null|(()=>void)};
 const archived=task("archived","done"),active=task("active","todo");
 window.localStorage.clear();window.history.replaceState(null,"","/");
 vi.stubGlobal("EventSource",class{onopen=null;onerror=null;constructor(){stream=this}addEventListener(_n:string,fn:()=>void){invalidate=fn}close(){}});
 const fetchMock=vi.fn(async(path:string)=>({ok:true,status:200,json:async()=>path==="/v1/task-cards"?[compactTask(active)]:path==="/v1/task-cards?column=done"?[compactTask(archived)]:path==="/v1/tasks/archived"?archived:[]}));
 vi.stubGlobal("fetch",fetchMock);render(<App/>);await screen.findByRole("button",{name:/TASK active/});
 const count=(path:string)=>fetchMock.mock.calls.filter(([p])=>p===path).length;
 expect(count("/v1/task-cards?column=done")).toBe(0);
 fireEvent.click(screen.getByRole("button",{name:"Show Done"}));
 fireEvent.click(await screen.findByRole("button",{name:/TASK archived/}));await screen.findByLabelText("Title");
 const listCount=count("/v1/task-cards?column=done"),detailCount=count("/v1/tasks/archived");
 await act(async()=>{invalidate();fireEvent.focus(window);stream.onerror?.();stream.onopen?.()});
 expect(count("/v1/task-cards?column=done")).toBe(listCount);
 expect(count("/v1/tasks/archived")).toBe(detailCount);
 await act(async()=>{await vi.advanceTimersByTimeAsync(90000)});
 expect(count("/v1/task-cards?column=done")).toBe(listCount);
 expect(count("/v1/tasks/archived")).toBe(detailCount);
 expect(screen.getByRole("button",{name:/TASK archived/})).toBeTruthy();
 fireEvent.click(screen.getByRole("button",{name:"Hide Done"}));fireEvent.click(screen.getByRole("button",{name:"Show Done"}));
 await waitFor(()=>expect(count("/v1/task-cards?column=done")).toBe(listCount+1));
});
it("opens a Done deep link without loading the archive",async()=>{
 const archived=task("archived","done");window.localStorage.clear();window.history.replaceState(null,"","/?task=archived");
 vi.stubGlobal("EventSource",class{onopen=null;onerror=null;addEventListener(){}close(){}});
 const fetchMock=vi.fn(async(path:string)=>({ok:true,status:200,json:async()=>path==="/v1/tasks/archived"?archived:[]}));vi.stubGlobal("fetch",fetchMock);
 render(<App/>);expect((await screen.findByLabelText("Title") as HTMLInputElement).value).toBe("archived");
 expect(fetchMock.mock.calls.some(([path])=>path==="/v1/task-cards?column=done")).toBe(false);
 fireEvent.focus(window);await act(async()=>{});
 expect(fetchMock.mock.calls.filter(([path])=>path==="/v1/tasks/archived")).toHaveLength(1);
});
it("offers retry when a Done deep link detail fails before a card is loaded",async()=>{
 window.localStorage.clear();window.history.replaceState(null,"","/?task=archived");
 vi.stubGlobal("EventSource",class{onopen=null;onerror=null;addEventListener(){}close(){}});
 let failed=true;const archived=task("archived","done");
 vi.stubGlobal("fetch",vi.fn(async(path:string)=>({ok:path!=="/v1/tasks/archived"||!failed,status:failed&&path==="/v1/tasks/archived"?503:200,json:async()=>path==="/v1/tasks/archived"?(failed?{error:"Unavailable"}:archived):[]})));
 render(<App/>);await screen.findByText("Unavailable");failed=false;
 fireEvent.click(screen.getByRole("button",{name:"Retry details"}));
 expect((await screen.findByLabelText("Title") as HTMLInputElement).value).toBe("archived");
});
it("does not resurrect an owned Done response after another client reopens the card",async()=>{
 window.localStorage.clear();window.history.replaceState(null,"","/");let current=task("fixture","todo");let invalidate=()=>{};
 vi.stubGlobal("EventSource",class{onopen=null;onerror=null;addEventListener(_n:string,fn:()=>void){invalidate=fn}close(){}});
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(path.endsWith("/move")){const body=JSON.parse(String(init?.body));current={...current,column:body.column,version:current.version+1};}
  const payload=path==="/v1/task-cards"?(current.column==="done"?[]:[compactTask(current)]):path==="/v1/task-cards?column=done"?(current.column==="done"?[compactTask(current)]:[]):path==="/v1/tasks/fixture"||path.endsWith("/move")?current:[];
  return {ok:true,status:200,json:async()=>payload};
 }));
 render(<App/>);fireEvent.click(await screen.findByRole("button",{name:/TASK fixture/}));await screen.findByLabelText("Title");
 fireEvent.click(screen.getByRole("combobox",{name:"Move task"}));fireEvent.click(screen.getByRole("option",{name:"Done"}));
 await waitFor(()=>expect(current.column).toBe("done"));
 fireEvent.click(screen.getByRole("button",{name:"Show Done"}));await screen.findByRole("button",{name:/TASK fixture/});
 current={...current,column:"todo",version:3};await act(async()=>{invalidate()});
 await waitFor(()=>expect(screen.getByRole("region",{name:"TODO column"}).querySelector('[data-task-id="fixture"]')).toBeTruthy());
 fireEvent.click(screen.getByRole("button",{name:"Hide Done"}));fireEvent.click(screen.getByRole("button",{name:"Show Done"}));
 await act(async()=>{});
 expect(screen.getAllByRole("button",{name:/TASK fixture/})).toHaveLength(1);
 expect(screen.getByRole("region",{name:"Done column"}).querySelector('[data-task-id="fixture"]')).toBeNull();
});
it("reloads a conflicting Done detail explicitly while preserving the edit draft",async()=>{
 window.localStorage.clear();window.history.replaceState(null,"","/?task=archived");let current=task("archived","done");let patches=0;
 vi.stubGlobal("EventSource",class{onopen=null;onerror=null;addEventListener(){}close(){}});
 const fetchMock=vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="PATCH"){patches++;return {ok:false,status:409,json:async()=>({error:"Card changed"})}}
  return {ok:true,status:200,json:async()=>path==="/v1/tasks/archived"?current:[]};
 });vi.stubGlobal("fetch",fetchMock);
 render(<App/>);const title=await screen.findByLabelText("Title");
 current={...current,title:"External title",version:2};
 fireEvent.change(title,{target:{value:"My draft"}});fireEvent.blur(title);
 await waitFor(()=>expect(patches).toBe(1));await act(async()=>{});
 expect(fetchMock.mock.calls.filter(([path,init])=>path==="/v1/tasks/archived"&&(!init?.method||init.method==="GET"))).toHaveLength(1);
 fireEvent.click(screen.getByRole("button",{name:"Reload details"}));
 await screen.findByRole("button",{name:"Reload latest overview"});
 expect((screen.getByLabelText("Title") as HTMLInputElement).value).toBe("My draft");
 expect(fetchMock.mock.calls.filter(([path,init])=>path==="/v1/tasks/archived"&&(!init?.method||init.method==="GET"))).toHaveLength(2);
});
