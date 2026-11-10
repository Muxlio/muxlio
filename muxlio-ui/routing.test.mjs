import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {test} from 'node:test';
import {once} from 'node:events';
import {createUIServer} from './server.mjs';

function router(url){
 const listeners={},changes=[],events=[];let location=new URL(url);
 const ctx=vm.createContext({URL,document:{addEventListener:(n,f)=>listeners[n]=f},
  get location(){return location},PopStateEvent:class{constructor(type){this.type=type}},
  window:{history:{pushState(_,__,path){location=new URL(path,location);changes.push(path)},replaceState(_,__,path){location=new URL(path,location);changes.push(path)}},dispatchEvent:e=>events.push(e.type)}});
 const source=readFileSync(new URL('./js/core.js',import.meta.url),'utf8').split('const $=')[0];
 vm.runInContext(source,ctx);
 return {ctx,listeners,changes,events,get url(){return location}};
}
test('normal paths use browser history, preserving encoded IDs and analytics queries',()=>{
 const r=router('http://localhost/runs');r.ctx.navigateTo('/runs/a%2Fb');
 assert.equal(r.url.pathname,'/runs/a%2Fb');assert.deepEqual(r.events,['popstate']);
 r.ctx.navigateTo('/analytics?range=7d&workflow=demo');assert.equal(r.ctx.currentRoute(),'/analytics?range=7d&workflow=demo');
 r.ctx.navigateTo('https://elsewhere.test/runs/x');assert.equal(r.changes.length,2);
 assert.equal(r.ctx.isUIPath('/v1/runs'),false);
});
test('legacy bookmarks migrate without adding a history entry',()=>{
 for(const [old,want] of [['#run/a%2Fb','/runs/a%2Fb'],['#/run/run-a','/runs/run-a'],['#analytics?range=7d','/analytics?range=7d'],['#worker-capability/w/generic%2Fecho/1','/worker-capabilities/w/generic%2Fecho/1']]){
  const r=router('http://localhost/'+old);r.ctx.migrateLegacyRoute();assert.equal(r.ctx.currentRoute(),want);assert.equal(r.url.hash,'');assert.equal(r.events.length,0);
 }
});
test('links preserve native modified clicks, external links and page anchors',()=>{
 const r=router('http://localhost/');let prevented=0;
 const link=href=>({target:'',hasAttribute:()=>false,getAttribute:()=>href});
 const click=(href,extra={})=>r.listeners.click({button:0,target:{closest:()=>link(href)},preventDefault(){prevented++},...extra});
 click('/runs/a');assert.equal(prevented,1);assert.equal(r.url.pathname,'/runs/a');
 click('/runs/b',{ctrlKey:true});click('https://elsewhere.test/runs/a');click('#worker-roster-local');
 assert.equal(prevented,1);assert.equal(r.url.pathname,'/runs/a');
});
test('development server serves direct app routes and keeps unknown APIs/assets as 404',async()=>{
 const server=createUIServer();server.listen(0,'127.0.0.1');await once(server,'listening');
 try{
  const root=`http://127.0.0.1:${server.address().port}`;
  for(const path of ['/runs','/runs/run-a','/analytics?range=7d','/transcripts/run-a/node%2Fa','/worker-capabilities/local/generic%2Fecho/1','/worker-capabilities/local/generic%2Fecho/']){
   const response=await fetch(root+path);assert.equal(response.status,200,path);assert.match(await response.text(),/<title>Muxlio/);
   const head=await fetch(root+path,{method:'HEAD'});assert.equal(head.status,200);assert.equal(await head.text(),'');
  }
  const page=await (await fetch(root+'/analytics')).text();
  for(const match of page.matchAll(/<script src="([^"]+)"/g)){const response=await fetch(root+match[1]);assert.equal(response.status,200,match[1]);assert.match(response.headers.get('content-type'),/javascript/)}
  for(const path of ['/missing','/js/not-real.js','/runs/a/unexpected'])assert.equal((await fetch(root+path)).status,404,path);
 }finally{await new Promise(resolve=>server.close(resolve))}
});
