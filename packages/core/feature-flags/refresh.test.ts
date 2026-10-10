// @vitest-environment node
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { refreshFeatureFlags } from "./queries";
it("restarts cold flag and plugin reads captured before a realtime change",async()=>{
 const qc=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const stops:Array<()=>void>=[];const old:Array<(data:{version:number})=>void>=[];
 for (const queryKey of [["feature-flags","ws"],["workspaces","ws","plugins","installed"]]) {
   const read=vi.fn().mockImplementationOnce(()=>new Promise(resolve=>old.push(resolve))).mockResolvedValue({version:2});
   stops.push(new QueryObserver(qc,{queryKey,queryFn:read}).subscribe(()=>{}));
 }
 await refreshFeatureFlags(qc,"ws");
 old.forEach(resolve=>resolve({version:1}));await Promise.resolve();
 expect(qc.getQueryData(["feature-flags","ws"])).toEqual({version:2});
 expect(qc.getQueryData(["workspaces","ws","plugins","installed"])).toEqual({version:2});
 stops.forEach(stop=>stop());qc.clear();
});
