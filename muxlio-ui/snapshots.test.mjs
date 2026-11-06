import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const source=readFileSync(new URL('./js/inspector.js',import.meta.url),'utf8');
const start=source.indexOf('let snapshotPollTimer=');
const end=source.indexOf('/* ── shared helpers',start);
const panel={innerHTML:'',querySelectorAll:()=>[]};
const calls=[];
const snapshot=(seq,digest)=>({seq,subjectId:'attempt',timestamp:'now',payload:{snapshot:{name:'report',digest}}});
const context={
  selectedActivation:'act',current:'run',TRANSCRIPT_POLL_MS:1200,
  clearTimeout(){},setTimeout(){throw Error('finished attempts must not poll')},
  $:()=>panel,esc:String,date:String,
  api:async path=>{
    calls.push(path);
    return calls.length===1
      ? {snapshots:[snapshot(3,'old')],nextAfter:3,hasMore:true}
      : {snapshots:[snapshot(5,'new')],nextAfter:5,hasMore:false};
  },
};
vm.createContext(context);
vm.runInContext(source.slice(start,end),context);
await context.loadInspectorSnapshots({id:'act',state:'failed'},'run');
assert.equal(calls.length,2);
assert.match(calls[1],/after=3/);
assert.match(panel.innerHTML,/not committed final outputs/);
assert.match(panel.innerHTML,/Latest · seq 5/);
assert.match(panel.innerHTML,/Historical · seq 3/);
assert.match(panel.innerHTML,/attempt/);
console.log('snapshot inspector pagination and provenance checks passed');
