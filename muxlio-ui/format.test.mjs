import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const format=vm.runInNewContext(`${readFileSync(new URL('./js/format.js',import.meta.url),'utf8')};MuxlioFormat`);

test('tokens share compact units and preserve zero versus unavailable',()=>{
 for(const [value,expected] of [[0,'0'],[999,'999'],[1000,'1.0K'],[1500000,'1.5M'],[1000000000,'1.0B'],[null,'Unavailable'],[undefined,'Unavailable'],[NaN,'Unavailable']])assert.equal(format.tokens(value),expected);
});

test('durations automatically scale from milliseconds through days',()=>{
 for(const [value,expected] of [[0,'0 ms'],[25,'25 ms'],[1000,'1.0 s'],[60000,'1m 0s'],[90000,'1m 30s'],[3600000,'1h 0m'],[5400000,'1h 30m'],[86400000,'1d 0h'],[90000000,'1d 1h'],[null,'Unavailable'],[Infinity,'Unavailable']])assert.equal(format.duration(value),expected);
 assert.equal(format.duration(90,'seconds'),'1m 30s');
 assert.equal(format.duration(90,'minutes'),'1h 30m');
 assert.equal(format.duration(25,'hours'),'1d 1h');
 assert.equal(format.duration(2,'days'),'2d 0h');
 assert.equal(format.elapsed('2026-01-01T00:00:00Z','2026-01-02T01:00:00Z'),'1d 1h');
 assert.equal(format.elapsed('invalid','2026-01-02'),'Unavailable');
 assert.equal(format.timestamp('invalid'),'Unavailable');
 assert.equal(format.timestamp('0001-01-01T00:00:00Z'),'Unavailable');
});
